package homeassistant

import (
	"sync"
	"time"
)

type AlarmState struct {
	State     string
	UpdatedAt time.Time
	Available bool
}
type Provider interface{ Current() AlarmState }
type Cache struct {
	mu     sync.RWMutex
	state  AlarmState
	maxAge time.Duration
}

func NewCache(max time.Duration) *Cache { return &Cache{maxAge: max} }
func (c *Cache) Set(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = AlarmState{s, time.Now(), s != "" && s != "unknown" && s != "unavailable"}
}
func (c *Cache) Current() AlarmState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.state
	if !s.Available || time.Since(s.UpdatedAt) > c.maxAge {
		s.Available = false
	}
	return s
}
