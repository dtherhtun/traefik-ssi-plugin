package ssi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest" // was sync?
	"time"
)

type Fetcher struct {
	next           http.Handler
	cache          *Cache
	breaker        *CircuitBreaker
	maxConcurrency int
	timeout        time.Duration
}

func NewFetcher(next http.Handler, cache *Cache, breaker *CircuitBreaker, maxConcurrency int, timeoutSeconds int) *Fetcher {
	return &Fetcher{
		next:           next,
		cache:          cache,
		breaker:        breaker,
		maxConcurrency: maxConcurrency,
		timeout:        time.Duration(timeoutSeconds) * time.Second,
	}
}

// FetchResolve resolves a list of includes concurrently and returns a map of results
// However, sticking to the "Streaming" plan:
// We might want to resolve them one by one or in batches?
// The plan said: "Spawn a goroutine... Push a future".
// Let's implement a single Fetch method that can be called concurrently.

// GetCached returns the content if it exists in the cache
func (f *Fetcher) GetCached(path string) ([]byte, bool) {
	return f.cache.Get(path)
}

func (f *Fetcher) Fetch(originalReq *http.Request, path string) ([]byte, error) {
	// 1. Check Cache
	if content, ok := f.cache.Get(path); ok {
		return content, nil
	}

	// 2. Check Breaker
	if !f.breaker.Allow() {
		return nil, fmt.Errorf("circuit breaker open")
	}

	// 3. Prepare Sub-request
	// We need to clone the request context?
	// We should create a new request based on the original one but with new path
	// AND ensuring we don't trigger an infinite loop if the partial includes itself (recursion limit is Non-Goal but good to be safe)
	// The prompt says "No recursive SSI".

	// We construct the request to go to 'next'.
	// We preserve headers? "Preserve original headers".
	// Yes, usually we want to forward cookies etc.

	ctx, cancel := context.WithTimeout(originalReq.Context(), f.timeout)
	defer cancel()

	subReq, err := http.NewRequestWithContext(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}

	// Copy headers
	for k, vv := range originalReq.Header {
		for _, v := range vv {
			subReq.Header.Add(k, v)
		}
	}
	// Remove Content-Length as we have no body
	subReq.ContentLength = 0
	subReq.Body = nil

	// Client vs Server request fields
	subReq.Host = originalReq.Host
	subReq.RequestURI = path

	// 4. Execute via 'next'
	recorder := httptest.NewRecorder()

	// We are trusting next.ServeHTTP is safe for concurrency.
	f.next.ServeHTTP(recorder, subReq)

	resp := recorder.Result()
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		f.breaker.RecordFailure()
		return nil, err
	}

	if resp.StatusCode >= 400 {
		f.breaker.RecordFailure()
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	f.breaker.RecordSuccess()

	// 5. Cache
	f.cache.Set(path, body)

	return body, nil
}
