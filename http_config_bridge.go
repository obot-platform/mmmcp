package mmmcp

import (
	"context"
	"crypto/rand"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/config"
)

const (
	privateConfigHeader = "X-MMMCP-Request-Config"
)

type configSelection struct {
	config     *config.Config
	useDefault bool
	dsn        string
	dsnSet     bool
}

type requestConfigRegistry struct {
	mu      sync.RWMutex
	entries map[string]configSelection
}

func (r *requestConfigRegistry) put(selection configSelection) string {
	token := rand.Text()
	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[string]configSelection)
	}
	r.entries[token] = selection
	r.mu.Unlock()
	return token
}

func (r *requestConfigRegistry) get(token string) (configSelection, bool) {
	r.mu.RLock()
	selection, ok := r.entries[token]
	r.mu.RUnlock()
	return selection, ok
}

func (r *requestConfigRegistry) delete(token string) {
	r.mu.Lock()
	delete(r.entries, token)
	r.mu.Unlock()
}

func (c *Composite) configBridge(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(privateConfigHeader)
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		selection := configSelection{useDefault: true}
		if cfg, ok := ConfigFromContext(r.Context()); ok {
			selection = configSelection{config: cfg}
		}
		if dsn, ok := DSNFromContext(r.Context()); ok {
			selection.dsn = dsn
			selection.dsnSet = true
		}
		token := c.requestConfigs.put(selection)
		defer c.requestConfigs.delete(token)
		r.Header.Set(privateConfigHeader, token)
		next.ServeHTTP(w, r)
	})
}

func (c *Composite) configMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			extra := request.GetExtra()
			if extra == nil || extra.Header == nil {
				return next(ctx, method, request)
			}
			selection, ok := c.requestConfigs.get(extra.Header.Get(privateConfigHeader))
			if !ok {
				return next(ctx, method, request)
			}
			if selection.useDefault {
				ctx = context.WithValue(ctx, configContextKey{}, c.defaultConfig)
			} else {
				ctx = ContextWithConfig(ctx, selection.config)
			}
			if selection.dsnSet {
				ctx = ContextWithDSN(ctx, selection.dsn)
			}
			return next(ctx, method, request)
		}
	}
}
