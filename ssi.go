package ssi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Enabled      bool     `json:"enabled,omitempty"`
	Types        []string `json:"types,omitempty"`
	MinFileSize  int      `json:"minFileSize,omitempty"`
	Silent       bool     `json:"silent,omitempty"`
	LastModified bool     `json:"lastModified,omitempty"`
	Concurrent   bool     `json:"concurrent,omitempty"`
}

func CreateConfig() *Config {
	return &Config{
		Enabled:      true,
		Types:        []string{"text/html"},
		MinFileSize:  0,
		Silent:       false,
		LastModified: false,
		Concurrent:   false,
	}
}

type SSIPlugin struct {
	next         http.Handler
	name         string
	config       *Config
	ssiRegex     *regexp.Regexp
	includeRegex *regexp.Regexp
	bufferPool   *sync.Pool
}

func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	if config.Types == nil || len(config.Types) == 0 {
		config.Types = []string{"text/html"}
	}

	return &SSIPlugin{
		next:   next,
		name:   name,
		config: config,
		// Match SSI directives: <!--#directive params -->
		ssiRegex:     regexp.MustCompile(`<!--#\s*(\w+)\s+([^>]+?)\s*-->`),
		includeRegex: regexp.MustCompile(`(\w+)="([^"]+)"`),
		bufferPool: &sync.Pool{
			New: func() interface{} {
				return new(bytes.Buffer)
			},
		},
	}, nil
}

type responseWriter struct {
	http.ResponseWriter
	body          *bytes.Buffer
	statusCode    int
	headerWritten bool
	plugin        *SSIPlugin
}

func (rw *responseWriter) WriteHeader(statusCode int) {
	if !rw.headerWritten {
		rw.statusCode = statusCode
		rw.headerWritten = true
	}
}

func (rw *responseWriter) Write(data []byte) (int, error) {
	if !rw.headerWritten {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.body.Write(data)
}

func (p *SSIPlugin) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	start := time.Now()
	if !p.config.Enabled {
		p.next.ServeHTTP(rw, req)
		return
	}

	buf := p.bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer p.bufferPool.Put(buf)

	customRW := &responseWriter{
		ResponseWriter: rw,
		body:           buf,
		statusCode:     http.StatusOK,
		plugin:         p,
	}

	p.next.ServeHTTP(customRW, req)

	contentType := customRW.Header().Get("Content-Type")
	if !p.shouldProcess(contentType, buf.Len()) {
		rw.WriteHeader(customRW.statusCode)
		_, _ = io.Copy(rw, buf)
		return
	}

	var processed []byte
	var err error

	if p.config.Concurrent {
		fmt.Println("Concurrent processing enabled")
		cache := make(map[string][]byte)
		processed, err = p.processSSIConcurrent(req, buf.Bytes(), cache)
	} else {
		fmt.Println("Concurrent processing disabled")
		processed, err = p.processSSI(req, buf.Bytes())
	}

	if err != nil && !p.config.Silent {
		http.Error(rw, fmt.Sprintf("SSI processing error: %v", err), http.StatusInternalServerError)
		return
	}

	customRW.Header().Set("Content-Length", fmt.Sprintf("%d", len(processed)))

	// Remove ETag if present (content has changed)
	customRW.Header().Del("ETag")

	if !p.config.LastModified {
		customRW.Header().Del("Last-Modified")
	}

	rw.WriteHeader(customRW.statusCode)
	_, _ = rw.Write(processed)
	duration := time.Since(start)
	fmt.Printf("%s %s %d %s\n", req.Method, req.URL.Path, customRW.statusCode, duration)
}

func (p *SSIPlugin) shouldProcess(contentType string, size int) bool {
	if size < p.config.MinFileSize {
		return false
	}

	for _, ct := range p.config.Types {
		if strings.HasPrefix(contentType, ct) {
			return true
		}
	}
	return false
}

type replacement struct {
	start int
	end   int
	data  []byte
	err   error
}

func (p *SSIPlugin) processSSIConcurrent(req *http.Request, content []byte, cache map[string][]byte) ([]byte, error) {
	matches := p.ssiRegex.FindAllSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content, nil
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	repls := make([]replacement, len(matches))

	for i, match := range matches {
		start, end := match[0], match[1]
		dirStart, dirEnd := match[2], match[3]
		paramStart, paramEnd := match[4], match[5]

		directive := string(content[dirStart:dirEnd])
		paramStr := string(content[paramStart:paramEnd])

		params := make(map[string]string)
		paramMatches := p.includeRegex.FindAllStringSubmatch(paramStr, -1)
		for _, pm := range paramMatches {
			if len(pm) >= 3 {
				params[pm[1]] = pm[2]
			}
		}

		if directive == "include" {
			wg.Add(1)
			go func(idx int, params map[string]string, start, end int) {
				defer wg.Done()

				uri := params["virtual"]
				if uri == "" {
					uri = params["file"]
				}

				// Check cache first (with lock)
				mu.Lock()
				cached, ok := cache[uri]
				mu.Unlock()

				var data []byte
				if ok {
					data = cached
				} else {
					data = p.handleInclude(req, params)
					// Store in cache (with lock)
					mu.Lock()
					cache[uri] = data
					mu.Unlock()
				}

				repls[idx] = replacement{start: start, end: end, data: data}
			}(i, params, start, end)
		} else {
			// Process other directives synchronously
			repls[i] = replacement{
				start: start,
				end:   end,
				data:  p.processDirective(directive, req, params),
			}
		}
	}

	wg.Wait()

	var result bytes.Buffer
	result.Grow(len(content)) // Pre-allocate
	lastIndex := 0

	for _, r := range repls {
		result.Write(content[lastIndex:r.start])
		result.Write(r.data)
		lastIndex = r.end
	}
	result.Write(content[lastIndex:])
	return result.Bytes(), nil
}

func (p *SSIPlugin) processSSI(req *http.Request, content []byte) ([]byte, error) {
	result := p.ssiRegex.ReplaceAllFunc(content, func(match []byte) []byte {
		directive, params := p.parseDirective(match)

		switch directive {
		case "include":
			return p.handleInclude(req, params)
		case "echo":
			return p.handleEcho(req, params)
		case "config":
			return p.handleConfig(params)
		case "set":
			return p.handleSet(req, params)
		case "if", "elif", "else", "endif":
			return []byte("")
		default:
			if p.config.Silent {
				return []byte("")
			}
			return []byte(fmt.Sprintf("[SSI: unknown directive '%s']", directive))
		}
	})

	return result, nil
}

func (p *SSIPlugin) parseDirective(match []byte) (string, map[string]string) {
	matches := p.ssiRegex.FindSubmatch(match)
	if len(matches) < 3 {
		return "", nil
	}

	directive := string(matches[1])
	paramsStr := string(matches[2])
	params := make(map[string]string)

	paramMatches := p.includeRegex.FindAllStringSubmatch(paramsStr, -1)
	for _, pm := range paramMatches {
		if len(pm) >= 3 {
			params[pm[1]] = pm[2]
		}
	}

	return directive, params
}

func (p *SSIPlugin) processDirective(directive string, req *http.Request, params map[string]string) []byte {
	switch directive {
	case "echo":
		return p.handleEcho(req, params)
	case "config":
		return p.handleConfig(params)
	case "set":
		return p.handleSet(req, params)
	case "if", "elif", "else", "endif":
		return []byte("")
	default:
		if p.config.Silent {
			return []byte("")
		}
		return []byte(fmt.Sprintf("[SSI: unknown directive '%s']", directive))
	}
}

func (p *SSIPlugin) handleInclude(req *http.Request, params map[string]string) []byte {
	var uri string
	isVirtual := false

	if v, ok := params["virtual"]; ok {
		uri = v
		isVirtual = true
	} else if v, ok := params["file"]; ok {
		uri = v
	} else {
		return p.errorMessage("include: no file or virtual specified")
	}

	subReq, err := http.NewRequest("GET", uri, nil)
	if err != nil {
		return p.errorMessage(fmt.Sprintf("include: failed to create request: %v", err))
	}

	subReq.Header = req.Header.Clone()

	if isVirtual {
		scheme := "http"
		if req.TLS != nil {
			scheme = "https"
		}
		host := req.Host
		subReq.URL.Scheme = scheme
		subReq.URL.Host = host
		subReq.URL.Path = uri
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	resp, err := client.Do(subReq)
	if err != nil {
		return p.errorMessage(fmt.Sprintf("include: request failed: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return p.errorMessage(fmt.Sprintf("include: status %d", resp.StatusCode))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return p.errorMessage(fmt.Sprintf("include: failed to read response: %v", err))
	}

	return body
}

func (p *SSIPlugin) handleEcho(req *http.Request, params map[string]string) []byte {
	varName, ok := params["var"]
	if !ok {
		return p.errorMessage("echo: no var specified")
	}

	value := p.getVariable(req, varName)
	if value == "" && !p.config.Silent {
		return []byte(fmt.Sprintf("[SSI: undefined variable '%s']", varName))
	}

	return []byte(value)
}

func (p *SSIPlugin) handleConfig(params map[string]string) []byte {
	return []byte("")
}

func (p *SSIPlugin) handleSet(req *http.Request, params map[string]string) []byte {
	return []byte("")
}

func (p *SSIPlugin) getVariable(req *http.Request, name string) string {
	switch name {
	case "DATE_LOCAL":
		return time.Now().Format("Monday, 02-Jan-2006 15:04:05 MST")
	case "DATE_GMT":
		return time.Now().UTC().Format("Monday, 02-Jan-2006 15:04:05 GMT")
	case "DOCUMENT_URI":
		return req.URL.Path
	case "QUERY_STRING":
		return req.URL.RawQuery
	case "REMOTE_ADDR":
		return req.RemoteAddr
	case "REQUEST_METHOD":
		return req.Method
	case "REQUEST_URI":
		return req.RequestURI
	case "SERVER_NAME":
		return req.Host
	case "HTTP_HOST":
		return req.Host
	case "HTTP_USER_AGENT":
		return req.Header.Get("User-Agent")
	case "HTTP_REFERER":
		return req.Header.Get("Referer")
	default:
		if strings.HasPrefix(name, "HTTP_") {
			headerName := strings.ReplaceAll(strings.TrimPrefix(name, "HTTP_"), "_", "-")
			return req.Header.Get(headerName)
		}
		return ""
	}
}

func (p *SSIPlugin) errorMessage(msg string) []byte {
	if p.config.Silent {
		return []byte("")
	}
	return []byte(fmt.Sprintf("[SSI Error: %s]", msg))
}
