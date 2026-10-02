package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/component"
	componenthttp "github.com/obot-platform/mmmcp/component/http"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/testserver"
	"golang.org/x/oauth2"
)

type staticOAuthHandler struct {
	token *oauth2.Token
}

func TestFactorySendsHeadersAndUsesFreshCallSessions(t *testing.T) {
	fixture := testserver.New(t, testserver.Options{
		Headers: map[string]string{"Authorization": "Bearer token"},
		Tools: []testserver.Tool{{
			Definition: &mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
			Handler: func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}}}, nil
			},
		}},
	})
	factory := componenthttp.NewFactory(componenthttp.FactoryOptions{})
	server := config.Server{Name: "fixture", URL: fixture.URL, Headers: map[string]string{"Authorization": "Bearer token"}}

	if _, err := factory.Discover(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := factory.CallTool(t.Context(), server, &mcp.CallToolParams{Name: "echo", Arguments: json.RawMessage(`{"value":1}`)})
		if err != nil {
			t.Fatal(err)
		}
		if got := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(got, `"value":1`) {
			t.Fatalf("result text = %q", got)
		}
	}
	if got := fixture.Discoveries(); got != 3 {
		t.Fatalf("downstream sessions = %d, want 3 (discovery plus two calls)", got)
	}
}

func TestFactoryPassesSelectedRequestHeadersAndStaticHeadersWin(t *testing.T) {
	fixture := testserver.New(t, testserver.Options{
		Headers: map[string]string{
			"Authorization": "Bearer configured",
			"X-Tenant":      "tenant-a",
		},
		Tools: []testserver.Tool{{
			Definition: &mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
			Handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			},
		}},
	})
	factory := componenthttp.NewFactory(componenthttp.FactoryOptions{})
	server := config.Server{
		Name:               "fixture",
		URL:                fixture.URL,
		Headers:            map[string]string{"Authorization": "Bearer configured"},
		PassthroughHeaders: []string{"Authorization", "X-Tenant"},
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer incoming")
	headers.Set("X-Tenant", "tenant-a")
	ctx := component.ContextWithRequestHeaders(t.Context(), headers)

	if _, err := factory.CallTool(ctx, server, &mcp.CallToolParams{Name: "echo"}); err != nil {
		t.Fatal(err)
	}
}

func TestFactoryUsesComponentOAuthHandler(t *testing.T) {
	fixture := testserver.New(t, testserver.Options{
		Headers: map[string]string{"Authorization": "Bearer oauth-token"},
	})
	var providedFor config.Server
	provider := componenthttp.OAuthHandlerProviderFunc(func(_ context.Context, server config.Server) (auth.OAuthHandler, error) {
		providedFor = server
		return staticOAuthHandler{token: &oauth2.Token{AccessToken: "oauth-token"}}, nil
	})
	factory := componenthttp.NewFactory(componenthttp.FactoryOptions{OAuth: provider})
	server := config.Server{Name: "oauth fixture", URL: fixture.URL}

	if _, err := factory.Discover(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	if providedFor.Name != server.Name || providedFor.URL != server.URL {
		t.Fatalf("provider server = %+v, want %+v", providedFor, server)
	}
}

func TestFactoryDiscoverSkipsUnadvertisedFeatureLists(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "tools-only", Version: "1.0.0"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "prompts/list", "resources/list", "resources/templates/list":
				return nil, fmt.Errorf("%s is not implemented", method)
			}
			return next(ctx, method, req)
		}
	})
	upstream := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer upstream.Close()

	features, err := componenthttp.NewFactory(componenthttp.FactoryOptions{}).Discover(t.Context(), config.Server{Name: "fixture", URL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(features.Tools) != 1 || features.Tools[0].Name != "echo" {
		t.Fatalf("tools = %+v, want [echo]", features.Tools)
	}
}

func (h staticOAuthHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return oauth2.StaticTokenSource(h.token), nil
}

func (staticOAuthHandler) Authorize(context.Context, *http.Request, *http.Response) error {
	return nil
}
