package component

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/config"
)

// Pool owns component runtimes scoped to an exact frontend session and config.
type Pool struct {
	factory RuntimeFactory

	mu              sync.Mutex
	entries         map[poolKey]*poolEntry
	watched         map[*mcp.ServerSession]struct{}
	closed          bool
	cleanup         sync.WaitGroup
	cleanupErr      error
	cleanupFailures uint64
}

type poolKey struct {
	frontend    *mcp.ServerSession
	fingerprint string
	component   string
}

type poolEntry struct {
	ready   chan struct{}
	runtime Runtime
	err     error
	refs    int
	onClose func()
}

// NewPool creates a stateful component runtime pool.
func NewPool(factory RuntimeFactory) *Pool {
	return &Pool{
		factory: factory,
		entries: make(map[poolKey]*poolEntry),
		watched: make(map[*mcp.ServerSession]struct{}),
	}
}

// Acquire returns the runtime bound to frontend, fingerprint, and server.
// The returned release function must be called after the operation finishes.
func (p *Pool) Acquire(ctx context.Context, frontend *mcp.ServerSession, fingerprint string, server config.Server, opts RuntimeOptions) (Runtime, func(), error) {
	if frontend == nil {
		return nil, nil, errors.New("component: nil frontend session")
	}
	key := poolKey{frontend: frontend, fingerprint: fingerprint, component: server.Name}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, nil, errors.New("component: pool is closed")
	}
	if entry := p.entries[key]; entry != nil {
		entry.refs++
		p.mu.Unlock()
		select {
		case <-entry.ready:
			if entry.err != nil {
				p.releaseEntry(key, entry)
				return nil, nil, entry.err
			}
			return entry.runtime, p.releaseFunc(key, entry), nil
		case <-ctx.Done():
			p.releaseEntry(key, entry)
			return nil, nil, ctx.Err()
		}
	}
	entry := &poolEntry{ready: make(chan struct{}), refs: 1, onClose: opts.OnClose}
	p.entries[key] = entry
	if _, ok := p.watched[frontend]; !ok {
		p.watched[frontend] = struct{}{}
		go func() {
			_ = frontend.Wait()
			p.closeSession(frontend)
		}()
	}
	p.mu.Unlock()

	entry.runtime, entry.err = p.factory.OpenRuntime(ctx, server, opts)
	p.mu.Lock()
	close(entry.ready)
	if entry.err != nil {
		delete(p.entries, key)
	}
	p.mu.Unlock()
	if entry.err != nil {
		p.releaseEntry(key, entry)
		return nil, nil, entry.err
	}
	if opts.OnOpen != nil {
		opts.OnOpen()
	}
	return entry.runtime, p.releaseFunc(key, entry), nil
}

func (p *Pool) releaseFunc(key poolKey, entry *poolEntry) func() {
	var once sync.Once
	return func() { once.Do(func() { p.releaseEntry(key, entry) }) }
}

func (p *Pool) releaseEntry(key poolKey, entry *poolEntry) {
	p.mu.Lock()
	if current := p.entries[key]; current == entry && entry.refs > 0 {
		entry.refs--
	}
	p.mu.Unlock()
}

// SetFrontendActivity updates the number of active HTTP requests for all
// runtimes bound to frontend.
func (p *Pool) SetFrontendActivity(frontend *mcp.ServerSession, active int) {
	if frontend == nil {
		return
	}
	var runtimes []FrontendActivityRuntime
	p.mu.Lock()
	for key, entry := range p.entries {
		if key.frontend != frontend {
			continue
		}
		select {
		case <-entry.ready:
			if runtime, ok := entry.runtime.(FrontendActivityRuntime); ok {
				runtimes = append(runtimes, runtime)
			}
		default:
		}
	}
	p.mu.Unlock()
	for _, runtime := range runtimes {
		runtime.SetFrontendActivity(active)
	}
}

func (p *Pool) closeSession(frontend *mcp.ServerSession) {
	var entries []*poolEntry
	p.mu.Lock()
	delete(p.watched, frontend)
	if p.closed {
		p.mu.Unlock()
		return
	}

	// Register before removing entries so Close also waits for detached cleanup.
	p.cleanup.Add(1)
	defer p.cleanup.Done()

	for key, entry := range p.entries {
		if key.frontend != frontend {
			continue
		}
		entries = append(entries, entry)
		delete(p.entries, key)
	}
	p.mu.Unlock()

	for _, entry := range entries {
		<-entry.ready
		if entry.runtime != nil {
			if err := entry.runtime.Close(); err != nil {
				// Keep the latest failure and a count without retaining past sessions' errors.
				p.mu.Lock()
				p.cleanupErr = err
				p.cleanupFailures++
				p.mu.Unlock()
			}
		}
		if entry.onClose != nil {
			entry.onClose()
		}
	}
}

// Close closes every pooled runtime and prevents future acquisitions.
func (p *Pool) Close() error {
	if p == nil {
		return nil
	}
	var entries []*poolEntry
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	for key, entry := range p.entries {
		entries = append(entries, entry)
		delete(p.entries, key)
	}
	p.mu.Unlock()
	var result error
	for _, entry := range entries {
		<-entry.ready
		if entry.runtime != nil {
			result = errors.Join(result, entry.runtime.Close())
		}
		if entry.onClose != nil {
			entry.onClose()
		}
	}
	p.cleanup.Wait()

	p.mu.Lock()
	if p.cleanupErr != nil {
		result = errors.Join(result, fmt.Errorf("%d runtime cleanup failures; last error: %w", p.cleanupFailures, p.cleanupErr))
	}
	p.mu.Unlock()

	return result
}
