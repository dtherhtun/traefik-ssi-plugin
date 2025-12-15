package ssi

import (
	"bufio"
	"net/http"
	"sync"
)

type Processor struct {
	rw             http.ResponseWriter
	bufferedRw     *bufio.Writer // Buffered writer
	req            *http.Request
	fetcher        *Fetcher
	scanner        *StreamScanner
	queue          chan interface{} // Can be []byte or *Include
	done           chan struct{}
	concurrencySem chan struct{} // To limit max 8 concurrent fetches per request
	wg             sync.WaitGroup
}

func NewProcessor(rw http.ResponseWriter, req *http.Request, fetcher *Fetcher) *Processor {
	p := &Processor{
		rw:             rw,
		bufferedRw:     bufio.NewWriterSize(rw, 16384), // 4KB → 16KB for better throughput
		req:            req,
		fetcher:        fetcher,
		scanner:        &StreamScanner{},
		queue:          make(chan interface{}, 256), // 100 → 256 to handle 44 includes + segments
		done:           make(chan struct{}),
		concurrencySem: make(chan struct{}, fetcher.maxConcurrency),
	}

	// Start the writer loop
	p.wg.Add(1)
	go p.processQueue()

	return p
}

func (p *Processor) Write(chunk []byte) (int, error) {
	// Parse the chunk into segments
	segments := p.scanner.Scan(chunk)
	for _, seg := range segments {
		switch seg.Type {
		case SegmentStatic:
			// No need to copy - segment content is already a separate slice from scanner
			p.queue <- seg.Content
		case SegmentInclude:
			// OPTIMIZATION: Check cache first to avoid goroutine overhead
			if cached, ok := p.fetcher.GetCached(seg.Include.Path); ok {
				// Even if cached, we must preserve order.
				// We can push the result directly to the queue if we wrap it in a channel
				// OR we can change queue to accept Result objects?
				// Current queue: chan interface{} (either []byte or chan []byte)
				// If we push []byte, processQueue treats it as static.
				// Wait! "processQueue" implementation:
				/*
				   		case []byte:
				              p.rw.Write(v)
				          case chan []byte:
				              content := <-v
				              p.rw.Write(content)
				*/
				// So if we push []byte, it gets written immediately.
				// Correct! Because queue is ordered.
				// If we push Static1, Include1(Cached), Static2.
				// We push []byte(Static1), []byte(Include1), []byte(Static2).
				// They are read in order.
				// BUT: SegmentStatic is pushed as []byte.
				// So yes, we can push []byte(cachedContent).
				p.queue <- cached
			} else {
				// Not cached, go async
				// Acquire semaphore before spawning goroutine to reduce overhead
				// when at max concurrency
				p.concurrencySem <- struct{}{}

				resultChan := make(chan []byte, 1)
				p.queue <- resultChan

				// Launch worker
				go func(inc *Include, rChan chan []byte) {
					//p.concurrencySem <- struct{}{}
					defer func() { <-p.concurrencySem }()

					data, err := p.fetcher.Fetch(p.req, inc.Path)
					if err != nil {
						rChan <- []byte("<!-- include error -->")
					} else {
						rChan <- data
					}
					close(rChan)
				}(seg.Include, resultChan)
			}
		}
	}
	return len(chunk), nil
}

func (p *Processor) Close() {
	// Flush any remaining static content
	segments := p.scanner.Flush()
	for _, seg := range segments {
		if seg.Type == SegmentStatic {
			p.queue <- seg.Content
		}
	}

	close(p.queue)
	// Wait for writer loop to finish
	p.wg.Wait()
}

func (p *Processor) processQueue() {
	defer p.wg.Done()
	defer p.bufferedRw.Flush() // Flush at end

	for item := range p.queue {
		switch v := item.(type) {
		case []byte:
			p.bufferedRw.Write(v)
		case chan []byte:
			// Only flush if buffer has significant content (>8KB) to avoid unnecessary syscalls
			// This reduces flush frequency while still getting data to client reasonably quickly
			if p.bufferedRw.Buffered() > 8192 {
				p.bufferedRw.Flush()
			}

			content := <-v
			p.bufferedRw.Write(content)
		}
	}
}
