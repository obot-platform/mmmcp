package stdio

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/component"
)

type fakeRuntime struct {
	id     int
	mu     sync.Mutex
	closed bool
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Duration
	timers []*fakeTimer
}

type fakeTimer struct {
	clock   *fakeClock
	at      time.Duration
	fn      func()
	stopped bool
	fired   bool
}

func TestManagerRetiresAndRestartsAfterIdle(t *testing.T) {
	clock := newFakeClock()
	var mu sync.Mutex
	var opened []*fakeRuntime
	manager := newManager(func(context.Context) (component.Runtime, error) {
		mu.Lock()
		defer mu.Unlock()
		runtime := &fakeRuntime{id: len(opened) + 1}
		opened = append(opened, runtime)
		return runtime, nil
	}, 30*time.Second, clock)

	first, release, err := manager.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
	clock.Advance(29 * time.Second)
	if opened[0].Closed() {
		t.Fatal("runtime retired before idle timeout")
	}
	clock.Advance(time.Second)
	if !opened[0].Closed() {
		t.Fatal("runtime was not retired at idle timeout")
	}
	second, release, err := manager.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if first == second || len(opened) != 2 {
		t.Fatalf("runtime did not restart: opened=%d", len(opened))
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestManagerDefersRetirementForOperationsAndFrontendActivity(t *testing.T) {
	clock := newFakeClock()
	runtime := &fakeRuntime{id: 1}
	manager := newManager(func(context.Context) (component.Runtime, error) { return runtime, nil }, time.Second, clock)

	_, release, err := manager.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Second)
	if runtime.Closed() {
		t.Fatal("active operation did not protect runtime")
	}
	release()
	manager.SetFrontendActivity(1)
	clock.Advance(2 * time.Second)
	if runtime.Closed() {
		t.Fatal("active frontend request did not protect runtime")
	}
	manager.SetFrontendActivity(0)
	clock.Advance(time.Second)
	if !runtime.Closed() {
		t.Fatal("runtime did not retire after all activity ended")
	}
}

func (*fakeRuntime) CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{}, nil
}
func (*fakeRuntime) GetPrompt(context.Context, *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
	return &mcp.GetPromptResult{}, nil
}
func (*fakeRuntime) ReadResource(context.Context, *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
	return &mcp.ReadResourceResult{}, nil
}
func (*fakeRuntime) Subscribe(context.Context, *mcp.SubscribeParams) error     { return nil }
func (*fakeRuntime) Unsubscribe(context.Context, *mcp.UnsubscribeParams) error { return nil }
func (r *fakeRuntime) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return nil
}
func (r *fakeRuntime) Closed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func newFakeClock() *fakeClock { return &fakeClock{} }

func (c *fakeClock) AfterFunc(delay time.Duration, fn func()) timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{clock: c, at: c.now + delay, fn: fn}
	c.timers = append(c.timers, timer)
	return timer
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}

func (c *fakeClock) Advance(delta time.Duration) {
	for {
		c.mu.Lock()
		c.now += delta
		delta = 0
		var due *fakeTimer
		for _, timer := range c.timers {
			if !timer.stopped && !timer.fired && timer.at <= c.now {
				due = timer
				timer.fired = true
				break
			}
		}
		c.mu.Unlock()
		if due == nil {
			return
		}
		due.fn()
	}
}
