package ssi

import (
	"sync/atomic"
	"time"
)

type CircuitBreaker struct {
	lastFailure  time.Time
	resetTimeout time.Duration
	failures     int32
	threshold    int32
	state        int32
}

const (
	StateClosed = 0 // Closed (healthy)
	StateOpen   = 1 // Open (broken)
)

func NewCircuitBreaker(threshold int, resetSeconds int) *CircuitBreaker {
	return &CircuitBreaker{
		threshold:    int32(threshold),
		resetTimeout: time.Duration(resetSeconds) * time.Second,
	}
}

func (cb *CircuitBreaker) Allow() bool {
	if atomic.LoadInt32(&cb.state) == StateOpen {
		if time.Since(cb.lastFailure) > cb.resetTimeout {
			// Half-open attempt or reset
			atomic.StoreInt32(&cb.state, StateClosed)
			atomic.StoreInt32(&cb.failures, 0)
			return true
		}
		return false
	}
	return true
}

func (cb *CircuitBreaker) RecordSuccess() {
	if atomic.LoadInt32(&cb.failures) > 0 {
		atomic.StoreInt32(&cb.failures, 0)
	}
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.lastFailure = time.Now()
	newFailures := atomic.AddInt32(&cb.failures, 1)
	if newFailures >= cb.threshold {
		atomic.StoreInt32(&cb.state, StateOpen)
	}
}
