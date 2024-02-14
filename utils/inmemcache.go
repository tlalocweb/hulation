package utils

import (
	"time"

	"github.com/tlalocweb/go-cache"
)

type InMemCache struct {
	cache         *cache.Cache
	expiration    time.Duration
	cleanInterval time.Duration
}

type InMemCacheParamChain struct {
	self *InMemCache
}

func NewInMemCache() *InMemCacheParamChain {
	newcache := &InMemCache{cache: nil}
	return &InMemCacheParamChain{self: newcache}
}

func (c *InMemCacheParamChain) WithExpiration(expiration time.Duration) *InMemCacheParamChain {
	c.self.expiration = expiration
	if c.self.cleanInterval == 0 {
		c.self.cleanInterval = expiration * 2
	}
	return c
}

func (c *InMemCacheParamChain) WithCleanInterval(cleanInterval time.Duration) *InMemCacheParamChain {
	c.self.cleanInterval = cleanInterval
	return c
}

func (c *InMemCacheParamChain) Start() *InMemCache {
	if c.self.expiration == 0 {
		c.self.expiration = 5 * time.Minute
	}
	if c.self.cleanInterval == 0 {
		c.self.cleanInterval = c.self.expiration * 2
	}
	c.self.cache = cache.New(c.self.expiration, c.self.cleanInterval)
	return c.self
}

func (c *InMemCache) Set(key string, value interface{}) {
	c.cache.Set(key, value, c.expiration)
}
func (c *InMemCache) SetAlways(key string, value interface{}) {
	c.cache.Set(key, value, cache.NoExpiration)
}

func (c *InMemCache) Get(key string) (interface{}, bool) {
	return c.cache.Get(key)
}

func (c *InMemCache) Del(key string) {
	c.cache.Delete(key)
}
