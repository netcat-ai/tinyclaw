package main

import (
	"sync"
	"time"
)

type cacheItem struct {
	value     []byte
	expiresAt time.Time
}

type ttlCache struct {
	mu    sync.Mutex
	items map[string]cacheItem
}

func newTTLCache() *ttlCache {
	return &ttlCache{
		items: make(map[string]cacheItem),
	}
}

func (c *ttlCache) Get(key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	item, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(item.expiresAt) {
		delete(c.items, key)
		return nil, false
	}
	return append([]byte(nil), item.value...), true
}

func (c *ttlCache) Set(key string, value []byte, ttl time.Duration) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[key] = cacheItem{
		value:     append([]byte(nil), value...),
		expiresAt: time.Now().Add(ttl),
	}
}

func (c *ttlCache) Has(key string) bool {
	_, ok := c.Get(key)
	return ok
}
