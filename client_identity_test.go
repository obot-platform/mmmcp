package mmmcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/config"
)

func TestOptionsClientInfoForHTTPComponents(t *testing.T) {
	for _, test := range []struct {
		name string
		info *mcp.Implementation
		want mcp.Implementation
	}{
		{name: "configured", info: &mcp.Implementation{Name: "Obot MCP Gateway", Version: "1.2.3"}, want: mcp.Implementation{Name: "Obot MCP Gateway", Version: "1.2.3"}},
		{name: "default", want: mcp.Implementation{Name: "mmmcp", Version: "dev"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			component, clientInfos := clientInfoComponent(t, false)
			composite, err := mmmcp.New(t.Context(), &config.Config{Servers: []config.Server{{Name: "component", URL: component.URL}}}, mmmcp.Options{ClientInfo: test.info})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = composite.Close() })
			frontend := httptest.NewServer(composite.HTTPHandler())
			t.Cleanup(frontend.Close)
			session := connectCurrent(t, frontend)
			t.Cleanup(func() { _ = session.Close() })
			if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo"}); err != nil {
				t.Fatal(err)
			}
			infos := clientInfos()
			if len(infos) != 1 || infos[0].Name != test.want.Name || infos[0].Version != test.want.Version {
				t.Fatalf("component client infos = %+v, want %+v", infos, test.want)
			}
		})
	}
}

func TestForwardClientInfoForHTTPComponents(t *testing.T) {
	for _, test := range []struct {
		name     string
		protocol string
		stateful bool
	}{
		{name: "stateless", protocol: "2026-07-28"},
		{name: "stateful", protocol: "2025-11-25", stateful: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			component, clientInfos := clientInfoComponent(t, test.stateful)
			composite, err := mmmcp.New(t.Context(), &config.Config{Servers: []config.Server{{Name: "component", URL: component.URL}}}, mmmcp.Options{
				ClientInfo:        &mcp.Implementation{Name: "gateway", Version: "1"},
				ForwardClientInfo: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = composite.Close() })
			frontend := httptest.NewServer(composite.HTTPHandler())
			t.Cleanup(frontend.Close)
			for _, identity := range []*mcp.Implementation{
				{Name: "first-client", Version: "1.2.3"},
				{Name: "second-client", Version: "4.5.6"},
			} {
				session := connectFrontend(t, frontend, identity, test.protocol)
				if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo"}); err != nil {
					t.Fatal(err)
				}
				if err := session.Close(); err != nil {
					t.Fatal(err)
				}
			}
			infos := clientInfos()
			if len(infos) != 2 || infos[0].Name != "first-client" || infos[0].Version != "1.2.3" || infos[1].Name != "second-client" || infos[1].Version != "4.5.6" {
				t.Fatalf("component client infos = %+v, want first-client/1.2.3 then second-client/4.5.6", infos)
			}
		})
	}
}

func connectFrontend(t *testing.T, frontend *httptest.Server, identity *mcp.Implementation, protocol string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(identity, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: frontend.URL, HTTPClient: frontend.Client(), DisableStandaloneSSE: true}, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func clientInfoComponent(t *testing.T, stateful bool) (*httptest.Server, func() []mcp.Implementation) {
	t.Helper()
	var mu sync.Mutex
	var infos []mcp.Implementation
	server := mcp.NewServer(&mcp.Implementation{Name: "component", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				if request, ok := req.(*mcp.CallToolRequest); ok && request.ClientInfo() != nil {
					value := *request.ClientInfo()
					mu.Lock()
					infos = append(infos, value)
					mu.Unlock()
				}
			}
			return next(ctx, method, req)
		}
	})
	fixture := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: !stateful, JSONResponse: true}))
	t.Cleanup(fixture.Close)
	return fixture, func() []mcp.Implementation {
		mu.Lock()
		defer mu.Unlock()
		return append([]mcp.Implementation(nil), infos...)
	}
}
