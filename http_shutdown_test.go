package mmmcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/testserver"
)

func TestCompositeCloseHTTPSessions(t *testing.T) {
	for _, mode := range []string{"list-only", "pooled", "active-stateful", "active-stateless"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			started := make(chan struct{})
			canceled := make(chan struct{})
			active := mode == "active-stateful" || mode == "active-stateless"
			fixture := testserver.New(t, testserver.Options{Stateful: mode != "active-stateless", Tools: []testserver.Tool{{
				Definition: &mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
				Handler: func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					if active {
						close(started)
						<-ctx.Done()
						close(canceled)

						return nil, ctx.Err()
					}

					return &mcp.CallToolResult{Content: []mcp.Content{}}, nil
				},
			}}})
			composite, err := New(ctx, &config.Config{Servers: []config.Server{{Name: "fixture", URL: fixture.URL}}}, Options{
				DSN: "sqlite://" + filepath.Join(t.TempDir(), "shutdown.db"),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = composite.Close() })
			frontend := httptest.NewServer(composite.HTTPHandler())
			t.Cleanup(frontend.Close)

			protocol := "2025-11-25"
			if mode == "active-stateless" {
				protocol = "2026-07-28"
			}
			client := mcp.NewClient(&mcp.Implementation{Name: "shutdown-test", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
				Endpoint: frontend.URL, HTTPClient: frontend.Client(), DisableStandaloneSSE: true,
			}, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			if _, err := session.ListTools(ctx, nil); err != nil {
				t.Fatal(err)
			}

			var streamDone chan struct{}
			if protocol == "2025-11-25" {
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, frontend.URL, nil)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Accept", "text/event-stream")
				request.Header.Set("Mcp-Session-Id", session.ID())
				response, err := frontend.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = response.Body.Close() })
				if response.StatusCode != http.StatusOK {
					t.Fatalf("SSE status = %d", response.StatusCode)
				}
				streamDone = make(chan struct{})
				go func() {
					_, _ = io.Copy(io.Discard, response.Body)
					close(streamDone)
				}()
			}

			callDone := make(chan error, 1)
			if mode != "list-only" {
				go func() {
					_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo"})
					callDone <- err
				}()
				if active {
					waitHTTPShutdown(t, started)
				} else if err := <-callDone; err != nil {
					t.Fatal(err)
				}
			}

			composite.bindings.mu.Lock()
			var servers []*mcp.Server
			for _, server := range composite.bindings.servers {
				servers = append(servers, server)
			}
			composite.bindings.mu.Unlock()

			closed := make(chan error, 1)
			go func() { closed <- composite.Close() }()
			select {
			case err := <-closed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Composite.Close did not finish")
			}

			if got := frontendBindingCounts(&composite.bindings); got != [3]int{} {
				t.Fatalf("bindings after Close = %v", got)
			}
			for _, server := range servers {
				for range server.Sessions() {
					t.Fatal("SDK retained a frontend session after Close")
				}
			}
			if streamDone != nil {
				waitHTTPShutdown(t, streamDone)
			}
			if active {
				waitHTTPShutdown(t, canceled)
				select {
				case err := <-callDone:
					if err == nil {
						t.Fatal("active call succeeded after shutdown")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("active call did not finish")
				}
			}
			if err := composite.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func waitHTTPShutdown(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for HTTP lifecycle event")
	}
}

func TestCompositeCloseDrainsAdmittedHTTPRequests(t *testing.T) {
	composite, err := New(t.Context(), &config.Config{}, Options{
		DSN: "sqlite://" + filepath.Join(t.TempDir(), "shutdown.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = composite.Close() })

	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", reader)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	requestDone := make(chan struct{})
	go func() {
		composite.HTTPHandler().ServeHTTP(httptest.NewRecorder(), request)
		close(requestDone)
	}()

	// A successful pipe write proves the request passed admission and is reading its body.
	if _, err := writer.Write([]byte("{")); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- composite.Close() }()
	waitHTTPShutdown(t, composite.shutdownCtx.Done())
	select {
	case err := <-closed:
		t.Fatalf("Close returned before the admitted request finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	rejected := httptest.NewRecorder()
	composite.HTTPHandler().ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "http://localhost/mcp", nil))
	if rejected.Code != http.StatusServiceUnavailable {
		t.Fatalf("request during shutdown status = %d", rejected.Code)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	waitHTTPShutdown(t, requestDone)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after the request returned")
	}
	if got := frontendBindingCounts(&composite.bindings); got != [3]int{} {
		t.Fatalf("bindings after draining = %v", got)
	}
}
