package ssi

import (
	"sync"
	"time"
)

type CacheItem struct {
	Expiration time.Time
	Content    []byte
}

type Cache struct {
	items sync.Map
	ttl   time.Duration
}

func NewCache(ttlSeconds int) *Cache {
	return &Cache{
		ttl: time.Duration(ttlSeconds) * time.Second,
	}
}

func (c *Cache) Get(key string) ([]byte, bool) {
	val, ok := c.items.Load(key)
	if !ok {
		return nil, false
	}
	item := val.(CacheItem)

	// Check expiration - this is necessary to avoid serving stale content
	// The cost is worth it for correctness
	if time.Now().After(item.Expiration) {
		c.items.Delete(key)
		return nil, false
	}

	return item.Content, true
}

func (c *Cache) Set(key string, content []byte) {
	c.items.Store(key, CacheItem{
		Content:    content,
		Expiration: time.Now().Add(c.ttl),
	})
}
