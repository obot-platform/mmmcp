package mmmcp

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/component"
)

type frontendImplementationContextKey struct{}

func (c *Composite) newHTTPHandler(opts Options) http.Handler {
	getServer := func(r *http.Request) *mcp.Server {
		return newFrontendServer(c, opts, frontendImplementationFromContext(r.Context()))
	}
	stateless := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		Logger:                       opts.Logger,
		PropagateRequestCancellation: true,
	})
	stateful := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		JSONResponse: true,
		Logger:       opts.Logger,
		EventStore:   c.events,
	})
	dispatch := &httpDispatcher{stateless: stateless, stateful: stateful}
	bridged := c.configBridge(c.selectFrontendImplementation(c.trackHTTPActivity(dispatch)))
	mcpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.closed.Load() {
			http.Error(w, "composite server is closed", http.StatusServiceUnavailable)
			return
		}
		forwardedHeaders := r.Header.Clone()
		forwardedHeaders.Del(authorizationErrorCaptureHeader)
		r = r.WithContext(component.ContextWithRequestHeaders(r.Context(), forwardedHeaders))
		if r.Method == http.MethodPost && !isSubscriptionsListenRequest(r) {
			c.serveBufferedAuthorizationResponse(w, r, bridged)
			return
		}
		bridged.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			c.serveHealth(w, r)
		case "/readyz":
			c.serveReady(w, r)
		default:
			mcpHandler.ServeHTTP(w, r)
		}
	})
}

func (c *Composite) selectFrontendImplementation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		cfg := effectiveConfig(r.Context(), c.defaultConfig)
		compiled, _, err := c.registry.Get(r.Context(), cfg)
		if err != nil {
			c.captureAuthorizationError(r.Context(), nil, err)
			http.Error(w, "frontend identity unavailable", http.StatusInternalServerError)
			return
		}
		implementation := frontendImplementation(cfg, compiled)
		ctx := context.WithValue(r.Context(), frontendImplementationContextKey{}, implementation)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func frontendImplementationFromContext(ctx context.Context) mcp.Implementation {
	if implementation, ok := ctx.Value(frontendImplementationContextKey{}).(mcp.Implementation); ok {
		return implementation
	}
	return mcp.Implementation{Name: implementationName, Version: implementationVersion}
}
