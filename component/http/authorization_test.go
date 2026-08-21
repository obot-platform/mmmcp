package http_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	componenthttp "github.com/obot-platform/mmmcp/component/http"
	"github.com/obot-platform/mmmcp/config"
	"golang.org/x/oauth2"
)

type failingOAuthHandler struct {
	err error
}

func TestFactoryReturnsAuthorizationError(t *testing.T) {
	tests := []struct {
		name             string
		status           int
		challenge        string
		resourceMetadata string
		scope            string
		scopes           []string
		errorCode        string
		errorDescription string
	}{
		{
			name:             "unauthorized",
			status:           http.StatusUnauthorized,
			challenge:        `Basic realm="legacy", Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource", scope="files:read files:write"`,
			resourceMetadata: "https://mcp.example.com/.well-known/oauth-protected-resource",
			scope:            "files:read files:write",
			scopes:           []string{"files:read", "files:write"},
		},
		{
			name:             "forbidden",
			status:           http.StatusForbidden,
			challenge:        `Bearer error="insufficient_scope", scope="files:write", resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource", error_description="File write permission required"`,
			resourceMetadata: "https://mcp.example.com/.well-known/oauth-protected-resource",
			scope:            "files:write",
			scopes:           []string{"files:write"},
			errorCode:        "insufficient_scope",
			errorDescription: "File write permission required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", tt.challenge)
				w.WriteHeader(tt.status)
			}))
			defer upstream.Close()

			factory := componenthttp.NewFactory(componenthttp.FactoryOptions{})
			_, err := factory.Discover(t.Context(), config.Server{Name: "protected", URL: upstream.URL})
			var authErr *componenthttp.AuthorizationError
			if !errors.As(err, &authErr) {
				t.Fatalf("error = %v, want *AuthorizationError", err)
			}
			if authErr.StatusCode != tt.status {
				t.Errorf("StatusCode = %d, want %d", authErr.StatusCode, tt.status)
			}
			if authErr.ResourceMetadata != tt.resourceMetadata {
				t.Errorf("ResourceMetadata = %q, want %q", authErr.ResourceMetadata, tt.resourceMetadata)
			}
			if authErr.Scope != tt.scope {
				t.Errorf("Scope = %q, want %q", authErr.Scope, tt.scope)
			}
			if !reflect.DeepEqual(authErr.Scopes, tt.scopes) {
				t.Errorf("Scopes = %v, want %v", authErr.Scopes, tt.scopes)
			}
			if authErr.ErrorCode != tt.errorCode {
				t.Errorf("ErrorCode = %q, want %q", authErr.ErrorCode, tt.errorCode)
			}
			if authErr.ErrorDescription != tt.errorDescription {
				t.Errorf("ErrorDescription = %q, want %q", authErr.ErrorDescription, tt.errorDescription)
			}
		})
	}
}

func TestFactoryReturnsAuthorizationErrorForMalformedChallenge(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer scope="unterminated`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	factory := componenthttp.NewFactory(componenthttp.FactoryOptions{})
	_, err := factory.Discover(t.Context(), config.Server{Name: "protected", URL: upstream.URL})
	var authErr *componenthttp.AuthorizationError
	if !errors.As(err, &authErr) {
		t.Fatalf("error = %v, want *AuthorizationError", err)
	}
	if authErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want %d", authErr.StatusCode, http.StatusUnauthorized)
	}
}

func TestFactoryPreservesAuthorizationErrorWhenOAuthFails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	oauthErr := errors.New("interactive authorization failed")
	provider := componenthttp.OAuthHandlerProviderFunc(func(context.Context, config.Server) (auth.OAuthHandler, error) {
		return failingOAuthHandler{err: oauthErr}, nil
	})
	factory := componenthttp.NewFactory(componenthttp.FactoryOptions{OAuth: provider})
	_, err := factory.Discover(t.Context(), config.Server{Name: "protected", URL: upstream.URL})
	if _, ok := errors.AsType[*componenthttp.AuthorizationError](err); !ok {
		t.Fatalf("error = %v, want *AuthorizationError", err)
	}
	if !errors.Is(err, oauthErr) {
		t.Fatalf("error = %v, want wrapped OAuth error", err)
	}
}

func (failingOAuthHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return nil, nil
}

func (h failingOAuthHandler) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	resp.Body.Close()
	return h.err
}
