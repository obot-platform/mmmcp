package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
	"golang.org/x/oauth2"
)

// AuthorizationError reports an authorization challenge returned by an HTTP
// component.
type AuthorizationError = component.AuthorizationError

// authorizationErrorHandler uses the SDK's authorization callback to retain
// the status and challenge parameters that its normal HTTP error discards.
type authorizationErrorHandler struct {
	server config.Server
}

// typedOAuthHandler preserves a component's challenge when its configured
// OAuth handler cannot complete authorization.
type typedOAuthHandler struct {
	auth.OAuthHandler
	server config.Server
}

func (authorizationErrorHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return nil, nil
}

func (h authorizationErrorHandler) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	defer resp.Body.Close()
	defer func() { _, _ = io.Copy(io.Discard, resp.Body) }()

	return authorizationErrorFromResponse(resp, h.server)
}

func (h typedOAuthHandler) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	authErr := authorizationErrorFromResponse(resp, h.server)
	if len(component.AuthorizationErrors(authErr)) == 0 {
		defer resp.Body.Close()

		return authErr
	}

	if err := h.OAuthHandler.Authorize(ctx, req, resp); err != nil {
		return errors.Join(authErr, err)
	}

	return nil
}

func authorizationErrorFromResponse(resp *http.Response, server config.Server) error {
	challenges := component.AuthorizationChallenges(resp)
	result := make([]error, 0, len(challenges))

	for _, challenge := range challenges {
		challenge.Component = server.Name
		challenge.URL = server.URL
		result = append(result, challenge)
	}

	if len(result) > 0 {
		return errors.Join(result...)
	}

	return fmt.Errorf("component HTTP request failed: %d", resp.StatusCode)
}
