package mmmcp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

type unauthorizedDiscoverer struct{}

func (unauthorizedDiscoverer) Discover(context.Context, config.Server) (*component.Features, error) {
	return nil, &component.AuthorizationError{StatusCode: http.StatusUnauthorized}
}

func deferredConfig() *config.Config {
	return &config.Config{DeferAuthorization: true, Servers: []config.Server{{Name: "oauth"}}}
}

func TestResolveToolCallAnswersAuthorizationLocally(t *testing.T) {
	registry := catalog.NewRegistry(unauthorizedDiscoverer{})
	defer registry.Close()
	composite := &Composite{registry: registry}
	resolved, ok, err := composite.ResolveToolCall(t.Context(), deferredConfig(), catalog.AuthorizationToolName, nil)
	if err != nil || !ok || resolved.Result == nil || resolved.Route != nil {
		t.Fatalf("resolved=%+v ok=%v err=%v", resolved, ok, err)
	}
	if text := resolved.Result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "oauth") {
		t.Fatalf("status = %q", text)
	}
}

func TestAuthorizationLinkErrorsStayPrivate(t *testing.T) {
	pending, err := catalog.Compile(t.Context(), deferredConfig(), unauthorizedDiscoverer{})
	if err != nil {
		t.Fatal(err)
	}
	composite := &Composite{serverOptions: Options{AuthorizationURLs: func(context.Context) ([]AuthorizationURL, error) {
		return nil, errors.New("dial postgres://admin:secret@db")
	}}}
	result := composite.authorizationStatus(t.Context(), pending)
	if text := result.Content[0].(*mcp.TextContent).Text; !result.IsError || strings.Contains(text, "secret") {
		t.Fatalf("host error leaked: %q", text)
	}
}

func TestInvalidAuthorizationLinkKeepsValidLinks(t *testing.T) {
	pending, err := catalog.Compile(t.Context(), deferredConfig(), unauthorizedDiscoverer{})
	if err != nil {
		t.Fatal(err)
	}
	composite := &Composite{serverOptions: Options{AuthorizationURLs: func(context.Context) ([]AuthorizationURL, error) {
		return []AuthorizationURL{{Message: "Broken", URL: "javascript:alert(1)"}, {Message: "Connect", URL: "https://example.com/authorize"}}, nil
	}}}
	result := composite.authorizationStatus(t.Context(), pending)
	text := result.Content[0].(*mcp.TextContent).Text
	if result.IsError || !strings.Contains(text, "Connect: <https://example.com/authorize>") || strings.Contains(text, "Broken") {
		t.Fatalf("result = %q", text)
	}
}

func TestAuthorizationLinkValidation(t *testing.T) {
	pending, err := catalog.Compile(t.Context(), deferredConfig(), unauthorizedDiscoverer{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		message string
		url     string
		valid   bool
	}{
		{name: "oauth query", message: "Connect", url: "https://example.com/authorize?state=a%2Bb&scope=mail", valid: true},
		{name: "relative", message: "Connect", url: "/authorize"},
		{name: "script", message: "Connect", url: "javascript:alert(1)"},
		{name: "credentials", message: "Connect", url: "https://user:secret@example.com/authorize"},
		{name: "markdown url", message: "Connect", url: "https://example.com/?state=>injected"},
		{name: "space", message: "Connect", url: "https://example.com/?q=a b"},
		{name: "markdown message", message: "[Click](https://evil.example)", url: "https://example.com/"},
		{name: "line separator", message: "Connect\u2028Trusted", url: "https://example.com/"},
		{name: "bare url message", message: "Sign in at https://evil.example", url: "https://example.com/"},
		{name: "www message", message: "Visit www.evil.example", url: "https://example.com/"},
		{name: "bidi override", message: "Connect\u202eevil", url: "https://example.com/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			composite := &Composite{serverOptions: Options{AuthorizationURLs: func(context.Context) ([]AuthorizationURL, error) {
				return []AuthorizationURL{{Message: test.message, URL: test.url}}, nil
			}}}
			result := composite.authorizationStatus(t.Context(), pending)
			text := result.Content[0].(*mcp.TextContent).Text
			if result.IsError == test.valid {
				t.Fatalf("valid=%v, result=%q", test.valid, text)
			}
			if !strings.Contains(text, "oauth") {
				t.Fatalf("result omits the pending component: %q", text)
			}
		})
	}
}
