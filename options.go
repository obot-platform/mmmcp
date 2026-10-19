package mmmcp

import (
	"context"
	"log/slog"
	nethttp "net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/component/http"
	"github.com/obot-platform/mmmcp/storage"
)

// Options configures the composite runtime.
type Options struct {
	Logger     *slog.Logger
	HTTPClient *nethttp.Client
	// ClientInfo identifies mmmcp to downstream components. Nil uses mmmcp/dev.
	ClientInfo *mcp.Implementation
	// ForwardClientInfo forwards the frontend client identity to downstream components.
	// ClientInfo is used when the frontend identity is unavailable.
	ForwardClientInfo bool
	// OAuth supplies a component-specific OAuth handler for downstream HTTP clients.
	OAuth http.OAuthHandlerProvider
	// AuthorizationURLs supplies sign-in links for deferred component OAuth.
	// It runs with the current frontend request context, only while discovery needs authorization.
	AuthorizationURLs func(context.Context) ([]AuthorizationURL, error)
	// DSN selects the default event storage. An empty DSN selects SQLite.
	DSN string
	// LookupEnv supplies the allow-listed baseline for command components.
	LookupEnv func(string) (string, bool)
	// CommandTerminateDuration controls each SDK command shutdown stage.
	CommandTerminateDuration time.Duration
	// PageSize limits each composite feature-list page. Zero returns a complete family.
	PageSize int
	// Storage controls SQL connection pools, data location, and event retention.
	Storage storage.Options
}

// AuthorizationURL identifies a component sign-in link shown by the authorization tool.
type AuthorizationURL struct {
	Message string
	URL     string
}
