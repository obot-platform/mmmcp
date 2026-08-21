package mmmcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/testserver"
)

type requestHeadersTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func TestCompositePassesSelectedFrontendHeaders(t *testing.T) {
	required := map[string]string{"X-Unlisted": ""}
	fixture := testserver.New(t, testserver.Options{
		Headers: required,
		Tools: []testserver.Tool{{
			Definition: &mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
			Handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			},
		}},
	})
	composite, err := mmmcp.New(t.Context(), &config.Config{Servers: []config.Server{{
		Name:               "fixture",
		URL:                fixture.URL,
		Headers:            map[string]string{"Authorization": "Bearer configured"},
		PassthroughHeaders: []string{"Authorization", "X-Tenant"},
	}}}, mmmcp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer composite.Close()

	// Startup discovery has no frontend request. Require the dynamic headers only
	// after the initial catalog is compiled.
	required["Authorization"] = "Bearer configured"
	required["X-Tenant"] = "tenant-a"

	frontend := httptest.NewServer(composite.HTTPHandler())
	defer frontend.Close()
	frontendHTTPClient := *frontend.Client()
	frontendHTTPClient.Transport = requestHeadersTransport{
		base: frontendHTTPClient.Transport,
		headers: http.Header{
			"Authorization": {"Bearer incoming"},
			"X-Tenant":      {"tenant-a"},
			"X-Unlisted":    {"must not leak"},
		},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             frontend.URL,
		HTTPClient:           &frontendHTTPClient,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo"}); err != nil {
		t.Fatal(err)
	}
}

func (t requestHeadersTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	for name, values := range t.headers {
		clone.Header.Del(name)
		for _, value := range values {
			clone.Header.Add(name, value)
		}
	}
	return t.base.RoundTrip(clone)
}
