package traefik_ssi_plugin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Config struct {
	RootPath      string   `json:"rootPath,omitempty"`
	Extensions    []string `json:"extensions,omitempty"`
	EnableVirtual bool     `json:"enableVirtual,omitempty"`
}

func CreateConfig() *Config {
	return &Config{
		RootPath:      "/var/www/html",
		Extensions:    []string{".shtml", ".html"},
		EnableVirtual: true,
	}
}

type SSIPlugin struct {
	next      http.Handler
	name      string
	config    *Config
	includeRe *regexp.Regexp
}

func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	if config.RootPath == "" {
		return nil, fmt.Errorf("rootPath cannot be empty")
	}

	includeRe := regexp.MustCompile(`<!--#include\s+(virtual|file)="([^"]+)"\s*-->`)

	return &SSIPlugin{
		next:      next,
		name:      name,
		config:    config,
		includeRe: includeRe,
	}, nil
}

func (p *SSIPlugin) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if !p.shouldProcess(req.URL.Path) {
		p.next.ServeHTTP(rw, req)
		return
	}

	recorder := &responseRecorder{
		ResponseWriter: rw,
		body:           &bytes.Buffer{},
		statusCode:     http.StatusOK,
	}

	p.next.ServeHTTP(recorder, req)

	contentType := recorder.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		recorder.writeRecorded(rw)
		return
	}

	processed, err := p.processSSI(recorder.body.Bytes(), req.URL.Path)
	if err != nil {
		http.Error(rw, fmt.Sprintf("SSI processing error: %v", err), http.StatusInternalServerError)
		return
	}

	if recorder.Header().Get("Content-Length") != "" {
		recorder.Header().Set("Content-Length", fmt.Sprintf("%d", len(processed)))
	}

	rw.WriteHeader(recorder.statusCode)
	rw.Write(processed)
}

func (p *SSIPlugin) shouldProcess(path string) bool {
	if len(p.config.Extensions) == 0 {
		return true
	}

	ext := filepath.Ext(path)
	for _, allowedExt := range p.config.Extensions {
		if ext == allowedExt {
			return true
		}
	}
	return false
}

func (p *SSIPlugin) processSSI(content []byte, currentPath string) ([]byte, error) {
	result := p.includeRe.ReplaceAllFunc(content, func(match []byte) []byte {
		matches := p.includeRe.FindSubmatch(match)
		if len(matches) < 3 {
			return match
		}

		includeType := string(matches[1])
		includePath := string(matches[2])

		var fullPath string
		if includeType == "virtual" && p.config.EnableVirtual {
			fullPath = filepath.Join(p.config.RootPath, includePath)
		} else {
			currentDir := filepath.Dir(filepath.Join(p.config.RootPath, currentPath))
			fullPath = filepath.Join(currentDir, includePath)
		}

		absRoot, _ := filepath.Abs(p.config.RootPath)
		absPath, _ := filepath.Abs(fullPath)
		if !strings.HasPrefix(absPath, absRoot) {
			return []byte(fmt.Sprintf("<!-- SSI Error: Access denied to %s -->", includePath))
		}

		includeContent, err := os.ReadFile(fullPath)
		if err != nil {
			return []byte(fmt.Sprintf("<!-- SSI Error: Cannot read %s: %v -->", includePath, err))
		}

		processed, err := p.processSSI(includeContent, includePath)
		if err != nil {
			return []byte(fmt.Sprintf("<!-- SSI Error: Processing %s: %v -->", includePath, err))
		}

		return processed
	})

	return result, nil
}

type responseRecorder struct {
	http.ResponseWriter
	body       *bytes.Buffer
	statusCode int
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	return r.body.Write(b)
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
}

func (r *responseRecorder) writeRecorded(rw http.ResponseWriter) {
	rw.WriteHeader(r.statusCode)
	io.Copy(rw, r.body)
}
