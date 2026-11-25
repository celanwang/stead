package simulation

import (
	"sync"
	"time"
)

type Clock struct {
	mu sync.Mutex
	at time.Time
}

func NewClock(at time.Time) *Clock { return &Clock{at: at.UTC()} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}
