package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/obot-platform/mmmcp/component"
	"golang.org/x/oauth2"
)

// AuthorizationError reports an authorization challenge returned by an HTTP
// component.
type AuthorizationError = component.AuthorizationError

// authorizationErrorHandler uses the SDK's authorization callback to retain
// the status and challenge parameters that its normal HTTP error discards.
type authorizationErrorHandler struct{}

// typedOAuthHandler preserves a component's challenge when its configured
// OAuth handler cannot complete authorization.
type typedOAuthHandler struct {
	auth.OAuthHandler
}

func (authorizationErrorHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return nil, nil
}

func (authorizationErrorHandler) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	defer resp.Body.Close()
	defer func() { _, _ = io.Copy(io.Discard, resp.Body) }()
	return authorizationErrorFromResponse(resp)
}

func (h typedOAuthHandler) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	authErr := authorizationErrorFromResponse(resp)
	if err := h.OAuthHandler.Authorize(ctx, req, resp); err != nil {
		return errors.Join(authErr, err)
	}
	return nil
}

func authorizationErrorFromResponse(resp *http.Response) *component.AuthorizationError {
	authErr := &component.AuthorizationError{StatusCode: resp.StatusCode}
	challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	if err != nil {
		return authErr
	}
	for _, challenge := range challenges {
		if challenge.Scheme != "bearer" {
			continue
		}
		if authErr.ResourceMetadata == "" {
			authErr.ResourceMetadata = challenge.Params["resource_metadata"]
		}
		if authErr.Scope == "" {
			authErr.Scope = challenge.Params["scope"]
			authErr.Scopes = strings.Fields(authErr.Scope)
		}
		if authErr.ErrorCode == "" {
			authErr.ErrorCode = challenge.Params["error"]
		}
		if authErr.ErrorDescription == "" {
			authErr.ErrorDescription = challenge.Params["error_description"]
		}
	}
	return authErr
}
