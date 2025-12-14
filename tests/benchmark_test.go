package tests

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/username/traefik-plugin-ssi/ssi"
)

// Benchmark parsing HTML with SSI includes
func BenchmarkParser_Scan(b *testing.B) {
	content := []byte(`<html><body><!--#include virtual="/header.html" -->Content<!--#include file="/footer.html" --></body></html>`)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		scanner := &ssi.StreamScanner{}
		segments := scanner.Scan(content)
		_ = segments
	}
}

// Benchmark cache operations
func BenchmarkCache_GetHit(b *testing.B) {
	cache := ssi.NewCache(300)
	cache.Set("/test.html", []byte("cached content"))

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = cache.Get("/test.html")
	}
}

func BenchmarkCache_GetMiss(b *testing.B) {
	cache := ssi.NewCache(300)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = cache.Get("/nonexistent.html")
	}
}

// Benchmark full SSI processing with multiple includes
func BenchmarkProcessor_Write(b *testing.B) {
	// Create mock backend
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		w.Write([]byte("<p>included content</p>"))
	})

	cache := ssi.NewCache(300)
	breaker := ssi.NewCircuitBreaker(5, 60)
	fetcher := ssi.NewFetcher(backend, cache, breaker, 8, 5)

	// HTML with 5 includes
	content := []byte(`<html>
		<!--#include virtual="/i1.html" -->
		<!--#include virtual="/i2.html" -->
		<!--#include virtual="/i3.html" -->
		<!--#include virtual="/i4.html" -->
		<!--#include virtual="/i5.html" -->
	</html>`)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		rw := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/test.html", nil)

		processor := ssi.NewProcessor(rw, req, fetcher)
		processor.Write(content)
		processor.Close()
	}
}

// Benchmark with cached includes (hot path)
func BenchmarkProcessor_WriteCached(b *testing.B) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		w.Write([]byte("<p>included</p>"))
	})

	cache := ssi.NewCache(300)
	breaker := ssi.NewCircuitBreaker(5, 60)
	fetcher := ssi.NewFetcher(backend, cache, breaker, 8, 5)

	// Pre-warm cache
	for i := 1; i <= 5; i++ {
		cache.Set("/warmup.html", []byte("<p>cached</p>"))
	}

	content := []byte(`<html>
		<!--#include virtual="/warmup.html" -->
		<!--#include virtual="/warmup.html" -->
		<!--#include virtual="/warmup.html" -->
		<!--#include virtual="/warmup.html" -->
		<!--#include virtual="/warmup.html" -->
	</html>`)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		rw := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/test.html", nil)

		processor := ssi.NewProcessor(rw, req, fetcher)
		processor.Write(content)
		processor.Close()
	}
}

// Benchmark large page with many includes (like index.html)
func BenchmarkProcessor_LargePage(b *testing.B) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		w.Write([]byte("<p>inc</p>"))
	})

	cache := ssi.NewCache(300)
	breaker := ssi.NewCircuitBreaker(5, 60)
	fetcher := ssi.NewFetcher(backend, cache, breaker, 8, 5)

	// Build HTML with 44 includes like index.html
	var buf bytes.Buffer
	buf.WriteString("<html><body>")
	for i := 1; i <= 44; i++ {
		buf.WriteString("<!--#include virtual=\"/cached.html\" -->")
	}
	buf.WriteString("</body></html>")
	content := buf.Bytes()

	// Pre-warm cache
	cache.Set("/cached.html", []byte("<p>cached</p>"))

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		rw := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/index.html", nil)

		processor := ssi.NewProcessor(rw, req, fetcher)
		processor.Write(content)
		processor.Close()
	}
}
