package mmmcp

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/testserver"
)

// Give in-memory sessions a reused ID and optional transport failure without HTTP retries.
type frontendBindingTransport struct {
	mcp.Transport
	readErr error
}

type frontendBindingConnection struct {
	mcp.Connection
	readErr error
}

func frontendBindingCounts(b *frontendBindings) [3]int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return [3]int{len(b.servers), len(b.sessions), len(b.watched)}
}

func waitFrontendBindingCounts(t *testing.T, b *frontendBindings, want [3]int) {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()

	for {
		if got := frontendBindingCounts(b); got == want {
			return
		}

		select {
		case <-tick.C:
		case <-timeout.C:
			t.Fatalf("binding counts (servers, sessions, watched) = %v, want %v", frontendBindingCounts(b), want)
		}
	}
}

func TestFrontendBindingsHTTP(t *testing.T) {
	for _, protocol := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(protocol, func(t *testing.T) {
			fixture := testserver.New(t, testserver.Options{Tools: []testserver.Tool{{
				Definition: &mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
				Handler: func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}}}, nil
				},
			}}})
			cfg := &config.Config{Servers: []config.Server{{Name: "fixture", Prefix: "demo", URL: fixture.URL}}}
			composite, err := New(t.Context(), cfg, Options{DSN: "sqlite://" + filepath.Join(t.TempDir(), "bindings.db")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = composite.Close() })

			frontend := httptest.NewServer(composite.HTTPHandler())
			t.Cleanup(frontend.Close)
			client := mcp.NewClient(&mcp.Implementation{Name: "bindings-test", Version: "1"}, nil)
			session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
				Endpoint: frontend.URL, HTTPClient: frontend.Client(), DisableStandaloneSSE: true,
			}, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })

			stateless := protocol == "2026-07-28"
			want := [3]int{1, 0, 1}
			if stateless {
				want = [3]int{}
			}
			if got := frontendBindingCounts(&composite.bindings); got != want {
				t.Fatalf("bindings after discovery = %v, want %v", got, want)
			}

			for range 3 {
				listed, err := session.ListTools(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(listed.Tools) != 1 || listed.Tools[0].Name != "demo__echo" {
					t.Fatalf("listed tools = %+v", listed.Tools)
				}

				if stateless {
					result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
						Name: "demo__echo", Arguments: map[string]any{"value": "ok"},
					})
					if err != nil {
						t.Fatal(err)
					}
					if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != `{"value":"ok"}` {
						t.Fatalf("tool result = %+v", result)
					}
				}

				if got := frontendBindingCounts(&composite.bindings); got != want {
					t.Fatalf("bindings after request = %v, want %v", got, want)
				}
			}

			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			waitFrontendBindingCounts(t, &composite.bindings, [3]int{})
		})
	}
}

func TestFrontendBindingsIgnoreMissingIdentity(t *testing.T) {
	var bindings frontendBindings
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	bindings.bindServer(nil, server)
	bindings.bindServer(&mcp.ServerSession{}, nil)
	bindings.bindServer(&mcp.ServerSession{}, server)
	bindings.bind(nil, "config", "component")
	bindings.unbind(nil, "config", "component")

	bindings.mu.Lock()
	defer bindings.mu.Unlock()
	if bindings.servers != nil || bindings.sessions != nil || bindings.watched != nil {
		t.Fatal("missing session identity initialized binding maps")
	}
}

func TestFrontendBindingsSessionLifetime(t *testing.T) {
	for _, test := range []struct {
		name    string
		readErr error
	}{
		{name: "normal"},
		{name: "transport-error", readErr: errors.New("broken test connection")},
	} {
		t.Run(test.name, func(t *testing.T) {
			var bindings frontendBindings
			server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			first, closeFirst := connectFrontendBindingSession(t, server, test.readErr)
			second, closeSecond := connectFrontendBindingSession(t, server, nil)
			if first == second || first.ID() != second.ID() {
				t.Fatal("test requires distinct sessions sharing one ID")
			}

			var binds sync.WaitGroup
			for range 10 {
				binds.Go(func() { bindings.bindServer(first, server) })
			}
			binds.Wait()
			if got := frontendBindingCounts(&bindings); got != [3]int{1, 0, 1} {
				t.Fatalf("repeated binds = %v", got)
			}

			for range 3 {
				bindings.bindServer(first, server)
				bindings.bind(first, "config", "one")
				bindings.bind(first, "config", "two")
				assertFrontendMatching(t, &bindings, "config", "one", server)
				assertFrontendMatching(t, &bindings, "other", "one", nil)

				bindings.unbind(first, "config", "one")
				assertFrontendMatching(t, &bindings, "config", "one", nil)
				assertFrontendMatching(t, &bindings, "config", "two", server)

				bindings.unbind(first, "config", "two")
				assertFrontendMatching(t, &bindings, "config", "two", nil)
				if got := frontendBindingCounts(&bindings); got != [3]int{0, 0, 1} {
					t.Fatalf("last-runtime unbind = %v", got)
				}
			}

			bindings.bindServer(first, server)
			bindings.bind(first, "config", "one")
			bindings.bindServer(second, server)
			bindings.bind(second, "config", "two")
			if got := frontendBindingCounts(&bindings); got != [3]int{2, 2, 2} {
				t.Fatalf("same-ID sessions = %v", got)
			}

			closeFirst()
			if err := first.Wait(); !errors.Is(err, test.readErr) {
				t.Fatalf("session Wait = %v, want %v", err, test.readErr)
			}
			waitFrontendBindingCounts(t, &bindings, [3]int{1, 1, 1})
			assertFrontendMatching(t, &bindings, "config", "one", nil)
			assertFrontendMatching(t, &bindings, "config", "two", server)

			closeSecond()
			waitFrontendBindingCounts(t, &bindings, [3]int{})
		})
	}
}

func assertFrontendMatching(t *testing.T, b *frontendBindings, fingerprint, component string, want *mcp.Server) {
	t.Helper()
	got := b.matching(fingerprint, component)
	if want == nil {
		if len(got) != 0 {
			t.Fatalf("matching(%q, %q) = %v, want empty", fingerprint, component, got)
		}
		return
	}

	if len(got) != 1 || got[0] != want {
		t.Fatalf("matching(%q, %q) = %v, want [%p]", fingerprint, component, got, want)
	}
}

func (t frontendBindingTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	connection, err := t.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}

	return frontendBindingConnection{Connection: connection, readErr: t.readErr}, nil
}

func (c frontendBindingConnection) SessionID() string { return "reused-id" }

func (c frontendBindingConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	message, err := c.Connection.Read(ctx)
	if err != nil && c.readErr != nil {
		return nil, c.readErr
	}

	return message, err
}

func connectFrontendBindingSession(t *testing.T, server *mcp.Server, readErr error) (*mcp.ServerSession, func()) {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	session, err := server.Connect(t.Context(), frontendBindingTransport{Transport: serverTransport, readErr: readErr}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	closeClient := func() { _ = clientSession.Close() }
	t.Cleanup(closeClient)

	return session, closeClient
}
