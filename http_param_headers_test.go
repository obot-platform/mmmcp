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
	"github.com/obot-platform/mmmcp/testserver"
)

type toolCallHeaderRecorder struct {
	base    http.RoundTripper
	mu      sync.Mutex
	headers http.Header
}

func (r *toolCallHeaderRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodPost {
		r.mu.Lock()
		r.headers = request.Header.Clone()
		r.mu.Unlock()
	}
	return r.base.RoundTrip(request)
}

func (r *toolCallHeaderRecorder) Headers() http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.headers.Clone()
}

func TestCompositeGeneratesToolParameterHeaders(t *testing.T) {
	for _, test := range []struct {
		name     string
		protocol string
		stateful bool
	}{
		{name: "stateless", protocol: "2026-07-28"},
		{name: "stateful", protocol: "2025-11-25", stateful: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &toolCallHeaderRecorder{base: http.DefaultTransport}
			fixture := testserver.New(t, testserver.Options{Stateful: test.stateful, Tools: []testserver.Tool{{
				Definition: &mcp.Tool{Name: "repository", InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"owner":   map[string]any{"type": "string", "x-mcp-header": "owner"},
						"enabled": map[string]any{"type": "boolean", "x-mcp-header": "enabled"},
						"count":   map[string]any{"type": "integer", "x-mcp-header": "count"},
						"scope": map[string]any{"type": "object", "properties": map[string]any{
							"region": map[string]any{"type": "string", "x-mcp-header": "region"},
						}},
					},
				}},
				Handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{}, nil
				},
			}}})
			composite, err := mmmcp.New(t.Context(), &config.Config{Servers: []config.Server{{Name: "fixture", URL: fixture.URL}}}, mmmcp.Options{
				HTTPClient: &http.Client{Transport: recorder},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = composite.Close() })
			frontend := httptest.NewServer(composite.HTTPHandler())
			t.Cleanup(frontend.Close)
			client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
			session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
				Endpoint:             frontend.URL,
				HTTPClient:           frontend.Client(),
				DisableStandaloneSSE: true,
			}, &mcp.ClientSessionOptions{ProtocolVersion: test.protocol})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })

			_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "repository", Arguments: map[string]any{
				"owner": " octocat ", "enabled": true, "count": 42, "scope": map[string]any{"region": "日本"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			headers := recorder.Headers()
			for name, want := range map[string]string{
				"Mcp-Param-owner":   "=?base64?IG9jdG9jYXQg?=",
				"Mcp-Param-enabled": "true",
				"Mcp-Param-count":   "42",
				"Mcp-Param-region":  "=?base64?5pel5pys?=",
			} {
				if got := headers.Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}
