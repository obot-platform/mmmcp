package component

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type blockingCloseRuntime struct {
	Runtime
	started chan struct{}
	finish  chan struct{}
	err     error
}

func TestPoolCleanupRetainsLastErrorAndCount(t *testing.T) {
	pool := NewPool(nil)
	frontend := &mcp.ServerSession{}
	firstErr := errors.New("first failure")
	lastErr := errors.New("last failure")
	ready := make(chan struct{})
	close(ready)

	for _, closeErr := range []error{firstErr, firstErr, lastErr, nil} {
		pool.entries[poolKey{frontend: frontend}] = &poolEntry{
			ready: ready,
			runtime: &blockingCloseRuntime{
				started: make(chan struct{}), finish: ready, err: closeErr,
			},
		}
		pool.closeSession(frontend)
	}

	if pool.cleanupErr != lastErr || pool.cleanupFailures != 3 {
		t.Fatalf("retained error = %v, count = %d; want last failure, 3", pool.cleanupErr, pool.cleanupFailures)
	}

	err := pool.Close()
	if !errors.Is(err, lastErr) || errors.Is(err, firstErr) {
		t.Fatalf("Close error = %v, want only the last cleanup error", err)
	}
	if !strings.Contains(err.Error(), "3 runtime cleanup failures") {
		t.Fatalf("Close error omits failure count: %v", err)
	}
}

func (r *blockingCloseRuntime) Close() error {
	close(r.started)
	<-r.finish

	return r.err
}

func TestPoolCloseWaitsForSessionCleanup(t *testing.T) {
	closeErr := errors.New("runtime cleanup failed")
	runtime := &blockingCloseRuntime{started: make(chan struct{}), finish: make(chan struct{}), err: closeErr}
	defer close(runtime.finish)

	var callbackFinished atomic.Bool
	frontend := &mcp.ServerSession{}
	ready := make(chan struct{})
	close(ready)
	pool := NewPool(nil)
	pool.entries[poolKey{frontend: frontend}] = &poolEntry{
		ready: ready, runtime: runtime,
		onClose: func() { callbackFinished.Store(true) },
	}

	go pool.closeSession(frontend)
	select {
	case <-runtime.started:
	case <-time.After(5 * time.Second):
		t.Fatal("session cleanup did not start")
	}

	closed := make(chan error, 1)
	go func() { closed <- pool.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Pool.Close returned before runtime cleanup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	runtime.finish <- struct{}{}
	select {
	case err := <-closed:
		if !errors.Is(err, closeErr) {
			t.Fatalf("Pool.Close error = %v, want %v", err, closeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Pool.Close did not finish")
	}
	if !callbackFinished.Load() {
		t.Fatal("Pool.Close returned before OnClose")
	}
}
