package ssi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	Enabled         bool     `json:"enabled,omitempty"`
	Types           []string `json:"types,omitempty"`
	MinFileSize     int      `json:"minFileSize,omitempty"`
	Silent          bool     `json:"silent,omitempty"`
	LastModified    bool     `json:"lastModified,omitempty"`
	Concurrent      bool     `json:"concurrent,omitempty"`
	CacheTTL        int      `json:"cacheTTL,omitempty"`
	IncludeTimeout  int      `json:"includeTimeout,omitempty"`
	MaxCacheSize    int      `json:"maxCacheSize,omitempty"`
	CircuitBreaker  bool     `json:"circuitBreaker,omitempty"`
	CBFailThreshold int      `json:"cbFailThreshold,omitempty"`
	CBResetTimeout  int      `json:"cbResetTimeout,omitempty"`
	MaxBodySize     int64    `json:"maxBodySize,omitempty"`
	MaxConcurrent   int      `json:"maxConcurrent,omitempty"`
}

func CreateConfig() *Config {
	return &Config{
		Enabled:         true,
		Types:           []string{"text/html"},
		MinFileSize:     0,
		Silent:          false,
		LastModified:    false,
		Concurrent:      true,
		CacheTTL:        300,
		IncludeTimeout:  5,
		MaxCacheSize:    1000,
		CircuitBreaker:  true,
		CBFailThreshold: 5,
		CBResetTimeout:  60,
		MaxBodySize:     10485760, // 10MB
		MaxConcurrent:   100,
	}
}

type cacheEntry struct {
	data      []byte
	expiresAt time.Time
}

type circuitState struct {
	failures    int32
	lastFailure int64
	isOpen      int32
}

type SSIPlugin struct {
	next           http.Handler
	name           string
	config         *Config
	ssiRegex       *regexp.Regexp
	includeRegex   *regexp.Regexp
	bufferPool     *sync.Pool
	includeClient  *http.Client
	cache          *sync.Map
	cacheSize      int32
	circuitBreaker *sync.Map
}

func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	if config.Types == nil || len(config.Types) == 0 {
		config.Types = []string{"text/html"}
	}

	if config.CacheTTL == 0 {
		config.CacheTTL = 300
	}
	if config.IncludeTimeout == 0 {
		config.IncludeTimeout = 5
	}
	if config.MaxCacheSize == 0 {
		config.MaxCacheSize = 1000
	}
	if config.CBFailThreshold == 0 {
		config.CBFailThreshold = 5
	}
	if config.CBResetTimeout == 0 {
		config.CBResetTimeout = 60
	}
	if config.MaxBodySize == 0 {
		config.MaxBodySize = 10485760
	}
	if config.MaxConcurrent == 0 {
		config.MaxConcurrent = 100
	}

	includeClient := &http.Client{
		Timeout: time.Duration(config.IncludeTimeout) * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:          200,
			MaxIdleConnsPerHost:   100,
			MaxConnsPerHost:       100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			DisableCompression:    false,
			DisableKeepAlives:     false,
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	plugin := &SSIPlugin{
		next:           next,
		name:           name,
		config:         config,
		ssiRegex:       regexp.MustCompile(`<!--#\s*(\w+)\s+([^>]+?)\s*-->`),
		includeRegex:   regexp.MustCompile(`(\w+)="([^"]+)"`),
		includeClient:  includeClient,
		cache:          &sync.Map{},
		circuitBreaker: &sync.Map{},
		bufferPool: &sync.Pool{
			New: func() interface{} {
				return new(bytes.Buffer)
			},
		},
	}

	if config.CacheTTL > 0 {
		go plugin.cacheCleanup(ctx)
	}

	return plugin, nil
}

func (p *SSIPlugin) cacheCleanup(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			p.cache.Range(func(key, value interface{}) bool {
				if entry, ok := value.(*cacheEntry); ok {
					if now.After(entry.expiresAt) {
						p.cache.Delete(key)
						atomic.AddInt32(&p.cacheSize, -1)
					}
				}
				return true
			})
		}
	}
}

type responseWriter struct {
	http.ResponseWriter
	body          *bytes.Buffer
	statusCode    int
	headerWritten bool
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
	if !p.config.Enabled {
		p.next.ServeHTTP(rw, req)
		return
	}

	buf := p.bufferPool.Get().(*bytes.Buffer)
	buf.Reset()

	customRW := &responseWriter{
		ResponseWriter: rw,
		body:           buf,
		statusCode:     http.StatusOK,
	}

	p.next.ServeHTTP(customRW, req)

	contentType := customRW.Header().Get("Content-Type")
	if !p.shouldProcess(contentType, buf.Len()) {
		rw.WriteHeader(customRW.statusCode)
		_, _ = io.Copy(rw, buf)
		p.bufferPool.Put(buf)
		return
	}

	content := make([]byte, buf.Len())
	copy(content, buf.Bytes())
	p.bufferPool.Put(buf)

	var processed []byte
	var err error

	if p.config.Concurrent {
		processed, err = p.processSSIConcurrent(req, content)
	} else {
		processed, err = p.processSSI(req, content)
	}

	if err != nil && !p.config.Silent {
		http.Error(rw, fmt.Sprintf("SSI processing error: %v", err), http.StatusInternalServerError)
		return
	}

	customRW.Header().Set("Content-Length", fmt.Sprintf("%d", len(processed)))
	customRW.Header().Del("ETag")

	if !p.config.LastModified {
		customRW.Header().Del("Last-Modified")
	}

	rw.WriteHeader(customRW.statusCode)
	_, _ = rw.Write(processed)
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
}

func (p *SSIPlugin) processSSIConcurrent(req *http.Request, content []byte) ([]byte, error) {
	matches := p.ssiRegex.FindAllSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content, nil
	}

	var wg sync.WaitGroup
	repls := make([]replacement, len(matches))
	semaphore := make(chan struct{}, p.config.MaxConcurrent)

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
				semaphore <- struct{}{}
				defer func() { <-semaphore }()

				data := p.handleInclude(req, params)
				repls[idx] = replacement{start: start, end: end, data: data}
			}(i, params, start, end)
		} else {
			repls[i] = replacement{
				start: start,
				end:   end,
				data:  p.processDirective(directive, req, params),
			}
		}
	}

	wg.Wait()

	var result bytes.Buffer
	result.Grow(len(content))
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

func (p *SSIPlugin) checkCircuitBreaker(uri string) bool {
	if !p.config.CircuitBreaker {
		return true
	}

	val, ok := p.circuitBreaker.Load(uri)
	if !ok {
		return true
	}

	state := val.(*circuitState)
	if atomic.LoadInt32(&state.isOpen) == 0 {
		return true
	}

	lastFailure := atomic.LoadInt64(&state.lastFailure)
	if time.Since(time.Unix(lastFailure, 0)) > time.Duration(p.config.CBResetTimeout)*time.Second {
		return true
	}

	return false
}

func (p *SSIPlugin) recordFailure(uri string) {
	if !p.config.CircuitBreaker {
		return
	}

	val, _ := p.circuitBreaker.LoadOrStore(uri, &circuitState{})
	state := val.(*circuitState)

	failures := atomic.AddInt32(&state.failures, 1)
	atomic.StoreInt64(&state.lastFailure, time.Now().Unix())

	if failures >= int32(p.config.CBFailThreshold) {
		if atomic.CompareAndSwapInt32(&state.isOpen, 0, 1) {
			if !p.config.Silent {
				log.Printf("[SSI] Circuit breaker opened for %s after %d failures", uri, failures)
			}
		}
	}
}

func (p *SSIPlugin) recordSuccess(uri string) {
	if !p.config.CircuitBreaker {
		return
	}

	val, ok := p.circuitBreaker.Load(uri)
	if !ok {
		return
	}

	state := val.(*circuitState)
	wasOpen := atomic.LoadInt32(&state.isOpen)

	if atomic.CompareAndSwapInt32(&state.isOpen, 1, 0) {
		if !p.config.Silent {
			log.Printf("[SSI] Circuit breaker closed for %s", uri)
		}
		atomic.StoreInt32(&state.failures, 0)
	} else if wasOpen == 0 {
		failures := atomic.LoadInt32(&state.failures)
		if failures > 0 {
			atomic.AddInt32(&state.failures, -1)
		}
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

	cacheKey := uri
	if isVirtual {
		scheme := "http"
		if req.TLS != nil {
			scheme = "https"
		}
		cacheKey = fmt.Sprintf("%s://%s%s", scheme, req.Host, uri)
	}

	if !p.checkCircuitBreaker(cacheKey) {
		return p.errorMessage(fmt.Sprintf("include: circuit breaker open for %s", uri))
	}

	if p.config.CacheTTL > 0 {
		if cached, ok := p.cache.Load(cacheKey); ok {
			entry := cached.(*cacheEntry)
			if time.Now().Before(entry.expiresAt) {
				return entry.data
			}
			p.cache.Delete(cacheKey)
			atomic.AddInt32(&p.cacheSize, -1)
		}
	}

	subReq, err := http.NewRequest("GET", uri, nil)
	if err != nil {
		p.recordFailure(cacheKey)
		return p.errorMessage(fmt.Sprintf("include: failed to create request: %v", err))
	}

	subReq.Header = req.Header.Clone()

	if isVirtual {
		scheme := "http"
		if req.TLS != nil {
			scheme = "https"
		}
		subReq.URL.Scheme = scheme
		subReq.URL.Host = req.Host
		subReq.URL.Path = uri
	}

	resp, err := p.includeClient.Do(subReq)
	if err != nil {
		p.recordFailure(cacheKey)
		return p.errorMessage(fmt.Sprintf("include: request failed: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.recordFailure(cacheKey)
		return p.errorMessage(fmt.Sprintf("include: status %d", resp.StatusCode))
	}

	limitedReader := io.LimitReader(resp.Body, p.config.MaxBodySize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		p.recordFailure(cacheKey)
		return p.errorMessage(fmt.Sprintf("include: failed to read response: %v", err))
	}

	p.recordSuccess(cacheKey)

	if p.config.CacheTTL > 0 {
		currentSize := atomic.LoadInt32(&p.cacheSize)
		if currentSize < int32(p.config.MaxCacheSize) {
			entry := &cacheEntry{
				data:      body,
				expiresAt: time.Now().Add(time.Duration(p.config.CacheTTL) * time.Second),
			}
			p.cache.Store(cacheKey, entry)
			atomic.AddInt32(&p.cacheSize, 1)
		}
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
