package clock

import (
	"context"
	"slices"
	"sort"
	"sync"
	"time"
)

// Clock says what time it is, and when a wait is over.
type Clock interface {
	Now() time.Time
	// After is a channel that receives once d has passed. A wait abandoned
	// before then costs nothing to the caller, whichever clock this is.
	After(d time.Duration) <-chan time.Time
}

// System is the real clock.
type System struct{}

func (System) Now() time.Time { return time.Now() }

func (System) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Sleep waits for d on the clock, or until ctx is done, whichever is first,
// and returns ctx's error in the second case.
func Sleep(ctx context.Context, clock Clock, d time.Duration) error {
	select {
	case <-clock.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stopped is a clock that only moves when it is told to.
//
// Each After registers a waiter, which Advance releases once the clock passes
// its deadline. Tests use BlockUntil to know a goroutine has reached its wait
// before moving time on - otherwise the Advance could land first and the wait
// would begin afterwards, from the new time - and Waiting to check how many
// waits remain.
type Stopped struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
	changed chan struct{} // closed, and replaced, whenever waiters changes
}

type waiter struct {
	deadline time.Time
	channel  chan time.Time
}

// NewStopped is a clock stopped at start.
func NewStopped(start time.Time) *Stopped {
	return &Stopped{now: start, changed: make(chan struct{})}
}

func (c *Stopped) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Stopped) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	channel := make(chan time.Time, 1)
	if d <= 0 {
		channel <- c.now
		return channel
	}
	c.waiters = append(c.waiters, waiter{deadline: c.now.Add(d), channel: channel})
	c.notify()
	return channel
}

// Advance moves the clock on by d, releasing every waiter whose deadline it
// reaches, earliest first.
func (c *Stopped) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	sort.SliceStable(c.waiters, func(i, j int) bool { return c.waiters[i].deadline.Before(c.waiters[j].deadline) })
	remaining := c.waiters[:0]
	for _, w := range c.waiters {
		if w.deadline.After(c.now) {
			remaining = append(remaining, w)
			continue
		}
		w.channel <- w.deadline
	}
	c.waiters = remaining
	c.notify()
}

// Waiting is how many waits are outstanding: registered and not yet
// released. Abandoned waits count until their deadline passes, as they would
// on a real timer.
func (c *Stopped) Waiting() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// BlockUntil waits until at least n waits are outstanding, or ctx is done.
func (c *Stopped) BlockUntil(ctx context.Context, n int) error {
	for {
		c.mu.Lock()
		count, changed := len(c.waiters), c.changed
		c.mu.Unlock()
		if count >= n {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Stopped) notify() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// Instant is a clock on which every wait is over as soon as it begins.
//
// After moves the clock on by d, records d, and releases at once. It is for
// code whose waits a test asserts on rather than steps through - a retry's
// backoff, a Retry-After honoured - where a Stopped clock would need a
// goroutine to Advance it at each wait. Now moves on with each wait, so a
// duration measured around one reads as the time it waited.
type Instant struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

// NewInstant is an instant clock starting at start.
func NewInstant(start time.Time) *Instant {
	return &Instant{now: start}
}

func (c *Instant) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Instant) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, d)
	c.now = c.now.Add(max(d, 0))
	channel := make(chan time.Time, 1)
	channel <- c.now
	return channel
}

// Waits is every wait asked for, in order, as asked.
func (c *Instant) Waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.waits)
}
