package http

import (
	"errors"
	"net/http"
	"testing"

	"github.com/obot-platform/mmmcp/config"
)

type authorizationResponseBody struct {
	read   bool
	closed bool
}

func (b *authorizationResponseBody) Read([]byte) (int, error) {
	b.read = true
	return 0, errors.New("unexpected response body read")
}

func (b *authorizationResponseBody) Close() error {
	b.closed = true
	return nil
}

func TestAuthorizationErrorHandlerClosesWithoutReading(t *testing.T) {
	body := &authorizationResponseBody{}
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header: http.Header{
			"Www-Authenticate": {`Bearer resource_metadata="https://example.com/meta", scope="files:read"`},
		},
		Body: body,
	}
	server := config.Server{
		Name: "protected",
		URL:  "https://example.com/mcp",
	}

	err := (authorizationErrorHandler{server: server}).Authorize(t.Context(), nil, resp)

	var authErr *AuthorizationError
	if !errors.As(err, &authErr) {
		t.Fatalf("Authorize() error = %v, want *AuthorizationError", err)
	}
	if authErr.StatusCode != http.StatusUnauthorized || authErr.Component != server.Name || authErr.URL != server.URL || authErr.ResourceMetadata != "https://example.com/meta" || authErr.Scope != "files:read" {
		t.Fatalf("Authorize() lost challenge details: %+v", authErr)
	}
	if body.read {
		t.Error("Authorize() read the response body")
	}
	if !body.closed {
		t.Error("Authorize() did not close the response body")
	}
}
