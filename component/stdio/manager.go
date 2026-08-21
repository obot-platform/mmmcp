package stdio

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/component"
)

const (
	defaultIdleTimeout = 30 * time.Second
)

var (
	_ component.Runtime                 = (*manager)(nil)
	_ component.FrontendActivityRuntime = (*manager)(nil)
)

type manager struct {
	open        func(context.Context) (component.Runtime, error)
	idleTimeout time.Duration
	clock       clock

	mu               sync.Mutex
	runtime          component.Runtime
	starting         chan struct{}
	activeOperations int
	frontendActivity int
	idleTimer        timer
	generation       uint64
	closed           bool
}

func newManager(open func(context.Context) (component.Runtime, error), idleTimeout time.Duration, clk clock) *manager {
	if idleTimeout <= 0 {
		idleTimeout = defaultIdleTimeout
	}
	if clk == nil {
		clk = realClock{}
	}
	return &manager{open: open, idleTimeout: idleTimeout, clock: clk}
}

func (m *manager) acquire(ctx context.Context) (component.Runtime, func(), error) {
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, nil, errors.New("stdio component runtime is closed")
		}
		m.stopTimerLocked()
		if m.runtime != nil {
			m.activeOperations++
			runtime := m.runtime
			m.mu.Unlock()
			return runtime, m.release, nil
		}
		if starting := m.starting; starting != nil {
			m.mu.Unlock()
			select {
			case <-starting:
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		starting := make(chan struct{})
		m.starting = starting
		m.mu.Unlock()

		runtime, err := m.open(ctx)
		m.mu.Lock()
		m.starting = nil
		if err == nil && !m.closed {
			m.runtime = runtime
			m.activeOperations++
		}
		closed := m.closed
		close(starting)
		m.mu.Unlock()
		if err != nil {
			return nil, nil, err
		}
		if closed {
			_ = runtime.Close()
			return nil, nil, errors.New("stdio component runtime is closed")
		}
		return runtime, m.release, nil
	}
}

func (m *manager) release() {
	m.mu.Lock()
	if m.activeOperations > 0 {
		m.activeOperations--
	}
	m.armTimerLocked()
	m.mu.Unlock()
}

func (m *manager) SetFrontendActivity(active int) {
	if active < 0 {
		active = 0
	}
	m.mu.Lock()
	m.frontendActivity = active
	if active > 0 {
		m.stopTimerLocked()
	} else {
		m.armTimerLocked()
	}
	m.mu.Unlock()
}

func (m *manager) stopTimerLocked() {
	if m.idleTimer != nil {
		m.idleTimer.Stop()
		m.idleTimer = nil
	}
	m.generation++
}

func (m *manager) armTimerLocked() {
	if m.closed || m.runtime == nil || m.activeOperations != 0 || m.frontendActivity != 0 || m.idleTimer != nil {
		return
	}
	m.generation++
	generation := m.generation
	m.idleTimer = m.clock.AfterFunc(m.idleTimeout, func() { m.retire(generation) })
}

func (m *manager) retire(generation uint64) {
	m.mu.Lock()
	if m.closed || generation != m.generation || m.activeOperations != 0 || m.frontendActivity != 0 {
		m.mu.Unlock()
		return
	}
	runtime := m.runtime
	if runtime == nil {
		m.idleTimer = nil
		m.mu.Unlock()
		return
	}
	m.runtime = nil
	m.idleTimer = nil
	m.generation++
	m.mu.Unlock()
	_ = runtime.Close()
}

func (m *manager) withRuntime(ctx context.Context, fn func(component.Runtime) error) error {
	runtime, release, err := m.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return fn(runtime)
}

func (m *manager) CallTool(ctx context.Context, params *mcp.CallToolParams) (result *mcp.CallToolResult, err error) {
	err = m.withRuntime(ctx, func(runtime component.Runtime) error {
		result, err = runtime.CallTool(ctx, params)
		return err
	})
	return result, err
}

func (m *manager) GetPrompt(ctx context.Context, params *mcp.GetPromptParams) (result *mcp.GetPromptResult, err error) {
	err = m.withRuntime(ctx, func(runtime component.Runtime) error {
		result, err = runtime.GetPrompt(ctx, params)
		return err
	})
	return result, err
}

func (m *manager) ReadResource(ctx context.Context, params *mcp.ReadResourceParams) (result *mcp.ReadResourceResult, err error) {
	err = m.withRuntime(ctx, func(runtime component.Runtime) error {
		result, err = runtime.ReadResource(ctx, params)
		return err
	})
	return result, err
}

func (m *manager) Subscribe(ctx context.Context, params *mcp.SubscribeParams) error {
	return m.withRuntime(ctx, func(runtime component.Runtime) error { return runtime.Subscribe(ctx, params) })
}

func (m *manager) Unsubscribe(ctx context.Context, params *mcp.UnsubscribeParams) error {
	return m.withRuntime(ctx, func(runtime component.Runtime) error { return runtime.Unsubscribe(ctx, params) })
}

func (m *manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.stopTimerLocked()
	runtime := m.runtime
	m.runtime = nil
	m.mu.Unlock()
	if runtime != nil {
		return runtime.Close()
	}
	return nil
}
