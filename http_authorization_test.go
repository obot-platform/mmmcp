package mmmcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/testserver"
)

type authorizationResponseRecorder struct {
	base http.RoundTripper
	mu   sync.Mutex
	auth []recordedAuthorizationResponse
}

type recordedAuthorizationResponse struct {
	status    int
	challenge string
}

func TestCompositePropagatesComponentAuthorizationResponse(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		challenge string
	}{
		{
			name:      "unauthorized",
			status:    http.StatusUnauthorized,
			challenge: `Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource", scope="files:read"`,
		},
		{
			name:      "forbidden",
			status:    http.StatusForbidden,
			challenge: `Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource", scope="files:write", error="insufficient_scope", error_description="File write permission required"`,
		},
	}
	protocols := []string{"2025-11-25", "2026-07-28"}

	for _, tt := range tests {
		for _, protocol := range protocols {
			t.Run(tt.name+"/"+protocol, func(t *testing.T) {
				fixture := testserver.New(t, testserver.Options{Tools: []testserver.Tool{{
					Definition: &mcp.Tool{Name: "tool", InputSchema: map[string]any{"type": "object"}},
					Handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						return &mcp.CallToolResult{}, nil
					},
				}}})
				target, err := url.Parse(fixture.URL)
				if err != nil {
					t.Fatal(err)
				}
				proxy := httputil.NewSingleHostReverseProxy(target)
				var protected atomic.Bool
				upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if protected.Load() {
						w.Header().Set("WWW-Authenticate", tt.challenge)
						w.WriteHeader(tt.status)
						return
					}
					proxy.ServeHTTP(w, r)
				})
				upstreamServer := testHTTPServer(t, upstream)

				composite, err := mmmcp.New(t.Context(), &config.Config{Servers: []config.Server{{Name: "fixture", URL: upstreamServer.URL}}}, mmmcp.Options{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = composite.Close() })
				protected.Store(true)
				frontend := testHTTPServer(t, composite.HTTPHandler())

				recorder := &authorizationResponseRecorder{base: frontend.Client().Transport}
				httpClient := &http.Client{Transport: recorder}
				client := mcp.NewClient(&mcp.Implementation{Name: "authorization-test", Version: "1"}, nil)
				session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
					Endpoint:             frontend.URL,
					HTTPClient:           httpClient,
					DisableStandaloneSSE: true,
				}, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
				if err != nil {
					t.Fatal(err)
				}
				defer session.Close()

				_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "tool"})
				if err == nil {
					t.Fatal("tool call unexpectedly succeeded")
				}
				status, challenge := recorder.authorizationResponse()
				if status != tt.status {
					t.Errorf("status = %d, want %d", status, tt.status)
				}
				if challenge != tt.challenge {
					t.Errorf("WWW-Authenticate = %q, want %q", challenge, tt.challenge)
				}
			})
		}
	}
}

func testHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func (r *authorizationResponseRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := r.base.RoundTrip(request)
	if response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
		r.mu.Lock()
		r.auth = append(r.auth, recordedAuthorizationResponse{
			status:    response.StatusCode,
			challenge: response.Header.Get("WWW-Authenticate"),
		})
		r.mu.Unlock()
	}
	return response, err
}

func (r *authorizationResponseRecorder) authorizationResponse() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.auth) == 0 {
		return 0, ""
	}
	last := r.auth[len(r.auth)-1]
	return last.status, last.challenge
}
