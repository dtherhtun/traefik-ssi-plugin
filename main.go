package traefik_plugin_ssi

import (
	"context"
	"net/http"
	"strings"

	"github.com/username/traefik-plugin-ssi/ssi"
)

// SSI plugin struct
type SSI struct {
	next    http.Handler
	config  *Config
	cache   *ssi.Cache
	breaker *ssi.CircuitBreaker
	name    string
}

// New created a new SSI plugin.
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	return &SSI{
		next:    next,
		name:    name,
		config:  config,
		cache:   ssi.NewCache(config.CacheTTLSeconds),
		breaker: ssi.NewCircuitBreaker(5, 60),
	}, nil
}

func (s *SSI) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	fetcher := ssi.NewFetcher(s.next, s.cache, s.breaker, s.config.MaxConcurrency, s.config.IncludeTimeoutSeconds)

	wrapper := &ResponseWrapper{
		rw:      rw,
		req:     req,
		fetcher: fetcher,
	}

	// Disable client-side caching validation to ensure we get full content for SSI processing
	req.Header.Del("If-Modified-Since")
	req.Header.Del("If-None-Match")

	s.next.ServeHTTP(wrapper, req)

	wrapper.Close()
}

type ResponseWrapper struct {
	rw          http.ResponseWriter
	req         *http.Request
	fetcher     *ssi.Fetcher
	processor   *ssi.Processor
	modeChecked bool
	isSSI       bool
	status      int
}

func (w *ResponseWrapper) Header() http.Header {
	return w.rw.Header()
}

func (w *ResponseWrapper) WriteHeader(statusCode int) {
	if !w.modeChecked {
		w.checkMode(statusCode)
	}
	w.status = statusCode

	if !w.isSSI {
		w.rw.WriteHeader(statusCode)
	}
	// If SSI, we delay WriteHeader until we are sure, or just suppress Content-Length
	if w.isSSI {
		w.rw.Header().Del("Content-Length")
		w.rw.Header().Del("ETag")
		w.rw.Header().Del("Last-Modified")
		w.rw.WriteHeader(statusCode)
	}
}

func (w *ResponseWrapper) Write(data []byte) (int, error) {
	if !w.modeChecked {
		w.checkMode(http.StatusOK)
		if w.status == 0 {
			w.WriteHeader(http.StatusOK)
		}
	}

	if w.isSSI {
		if w.processor == nil {
			w.processor = ssi.NewProcessor(w.rw, w.req, w.fetcher)
		}
		return w.processor.Write(data)
	}

	return w.rw.Write(data)
}

func (w *ResponseWrapper) checkMode(code int) {
	w.modeChecked = true

	ct := w.rw.Header().Get("Content-Type")

	if code < 200 || code >= 300 {
		return
	}

	if ct == "" {
		return
	}

	if !strings.Contains(ct, "text/html") && !strings.Contains(ct, "application/xhtml+xml") {
		return
	}

	ce := w.rw.Header().Get("Content-Encoding")
	if ce != "" && ce != "identity" {
		return
	}

	w.isSSI = true
}

func (w *ResponseWrapper) Flush() {
	if f, ok := w.rw.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *ResponseWrapper) Close() {
	if w.processor != nil {
		w.processor.Close()
	}
}
