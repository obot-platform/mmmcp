package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/component"
	componenthttp "github.com/obot-platform/mmmcp/component/http"
	"github.com/obot-platform/mmmcp/config"
)

// reapingServer mimics a component with an idle-session reaper: reaping drops
// the standalone GET stream, and later requests with that session ID get
// reapStatus. mcp-grocy answers 400; the spec requires 404.
type reapingServer struct {
	inner      http.Handler
	reapStatus int

	mu        sync.Mutex
	reaped    map[string]bool
	getCancel map[string]context.CancelFunc
}

func (s *reapingServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get("Mcp-Session-Id")
	s.mu.Lock()
	if id != "" && s.reaped[id] {
		s.mu.Unlock()
		http.Error(w, "invalid or expired session ID", s.reapStatus)
		return
	}
	if r.Method == http.MethodGet && id != "" {
		ctx, cancel := context.WithCancel(r.Context())
		s.getCancel[id] = cancel
		r = r.WithContext(ctx)
	}
	s.mu.Unlock()
	s.inner.ServeHTTP(w, r)
}

func (s *reapingServer) reapAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, cancel := range s.getCancel {
		s.reaped[id] = true
		cancel()
	}
}

// Regression test for obot-platform/obot#8539: a pooled runtime whose
// downstream session was reaped must be replaced, not reused forever.
func TestPoolReplacesRuntimeAfterReapedSession(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "reaper", Version: "1"}, nil)
			mcp.AddTool(server, &mcp.Tool{Name: "ping"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
			})
			fake := &reapingServer{
				inner:      mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil),
				reapStatus: status,
				reaped:     map[string]bool{},
				getCancel:  map[string]context.CancelFunc{},
			}
			ts := httptest.NewServer(fake)
			t.Cleanup(ts.Close)

			// The pool keys runtimes by a live frontend session.
			st, ct := mcp.NewInMemoryTransports()
			frontend, err := mcp.NewServer(&mcp.Implementation{Name: "frontend", Version: "1"}, nil).Connect(t.Context(), st, nil)
			if err != nil {
				t.Fatal(err)
			}
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "1"}, nil).Connect(t.Context(), ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cs.Close() })

			pool := component.NewPool(componenthttp.NewFactory(componenthttp.FactoryOptions{}))
			t.Cleanup(func() { _ = pool.Close() })
			cfg := config.Server{Name: "reaper", URL: ts.URL}

			var opened, closed int
			call := func() (component.Runtime, error) {
				rt, release, err := pool.Acquire(t.Context(), frontend, "fp", cfg, component.RuntimeOptions{
					OnOpen:  func() { opened++ },
					OnClose: func() { closed++ },
				})
				if err != nil {
					return nil, err
				}
				defer release()
				_, err = rt.CallTool(t.Context(), &mcp.CallToolParams{Name: "ping"})
				return rt, err
			}

			first, err := call()
			if err != nil {
				t.Fatalf("first call: %v", err)
			}
			done, ok := first.(component.DoneRuntime)
			if !ok {
				t.Fatalf("HTTP runtime %T does not implement component.DoneRuntime", first)
			}

			// Wait for the standalone GET stream before reaping it.
			deadline := time.Now().Add(5 * time.Second)
			for {
				fake.mu.Lock()
				n := len(fake.getCancel)
				fake.mu.Unlock()
				if n > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("standalone SSE stream was not opened")
				}
				time.Sleep(10 * time.Millisecond)
			}
			fake.reapAll()

			// The SDK fails the connection when the SSE reconnect is rejected.
			select {
			case <-done.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("runtime did not end after its session was reaped")
			}

			for i := range 2 {
				rt, err := call()
				if err != nil {
					t.Fatalf("call %d after reap: %v", i+1, err)
				}
				if rt == first {
					t.Fatalf("call %d after reap reused the ended runtime", i+1)
				}
			}
			// The replacement keeps the dead runtime's frontend binding, so it is
			// not released (OnClose) on replacement.
			if opened != 2 || closed != 0 {
				t.Fatalf("OnOpen = %d, OnClose = %d; want 2 and 0", opened, closed)
			}
		})
	}
}
