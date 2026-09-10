package mmmcp

import (
	"context"
	"reflect"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

type configToolSubscriptions struct {
	mu      sync.Mutex
	configs map[string]*configToolSubscription
}

type configToolSubscription struct {
	catalog     *catalog.Catalog
	fingerprint string
	servers     map[*mcp.Server]struct{}
}

type frontendBinding struct {
	fingerprint string
	component   string
}

type frontendBindings struct {
	mu       sync.Mutex
	sessions map[*mcp.ServerSession]map[frontendBinding]struct{}
	servers  map[*mcp.ServerSession]*mcp.Server
}

// observe retains snapshots only while a configuration has active listeners.
func (s *configToolSubscriptions) observe(id, method string, server *mcp.Server, compiled *catalog.Catalog, fingerprint string) func() {
	s.mu.Lock()
	entry := s.configs[id]
	var notify []*mcp.Server
	if entry != nil {
		if entry.fingerprint != fingerprint && !reflect.DeepEqual(entry.catalog.Tools(), compiled.Tools()) {
			for listener := range entry.servers {
				notify = append(notify, listener)
			}
		}
		entry.catalog, entry.fingerprint = compiled, fingerprint
	}
	var cleanup func()
	if method == subscriptionsListenMethod {
		if entry == nil {
			entry = &configToolSubscription{catalog: compiled, fingerprint: fingerprint, servers: make(map[*mcp.Server]struct{})}
			if s.configs == nil {
				s.configs = make(map[string]*configToolSubscription)
			}
			s.configs[id] = entry
		}
		entry.servers[server] = struct{}{}
		cleanup = func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(entry.servers, server)
			if len(entry.servers) == 0 {
				delete(s.configs, id)
			}
		}
	}
	s.mu.Unlock()
	for _, listener := range notify {
		notifyFeatureChanged(listener, component.FeatureTools)
	}
	return cleanup
}

func (b *frontendBindings) bindServer(session *mcp.ServerSession, server *mcp.Server) {
	if session == nil || server == nil {
		return
	}
	b.mu.Lock()
	if b.servers == nil {
		b.servers = make(map[*mcp.ServerSession]*mcp.Server)
	}
	b.servers[session] = server
	b.mu.Unlock()
}

func (b *frontendBindings) bind(session *mcp.ServerSession, fingerprint, componentName string) {
	if session == nil {
		return
	}
	b.mu.Lock()
	if b.sessions == nil {
		b.sessions = make(map[*mcp.ServerSession]map[frontendBinding]struct{})
	}
	if b.sessions[session] == nil {
		b.sessions[session] = make(map[frontendBinding]struct{})
	}
	b.sessions[session][frontendBinding{fingerprint: fingerprint, component: componentName}] = struct{}{}
	b.mu.Unlock()
}

func (b *frontendBindings) unbind(session *mcp.ServerSession, fingerprint, componentName string) {
	b.mu.Lock()
	bindings := b.sessions[session]
	delete(bindings, frontendBinding{fingerprint: fingerprint, component: componentName})
	if len(bindings) == 0 {
		delete(b.sessions, session)
		delete(b.servers, session)
	}
	b.mu.Unlock()
}

func (b *frontendBindings) matching(fingerprint, componentName string) []*mcp.Server {
	b.mu.Lock()
	defer b.mu.Unlock()
	var result []*mcp.Server
	key := frontendBinding{fingerprint: fingerprint, component: componentName}
	for session, bindings := range b.sessions {
		if _, ok := bindings[key]; ok && b.servers[session] != nil {
			result = append(result, b.servers[session])
		}
	}
	return result
}

func (c *Composite) bindFrontendServer(server *mcp.Server) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if frontend, ok := req.GetSession().(*mcp.ServerSession); ok {
				c.bindings.bindServer(frontend, server)
			}
			return next(ctx, method, req)
		}
	}
}

func (c *Composite) callbacksForComponent(frontend *mcp.ServerSession, cfg *config.Config, fingerprint, componentName, prefix string) component.Callbacks {
	callbacks := component.Callbacks{Frontend: frontend, Component: componentName}
	callbacks.Progress = func(ctx context.Context, params *mcp.ProgressNotificationParams) {
		if frontend != nil && params != nil {
			_ = frontend.NotifyProgress(ctx, params)
		}
	}
	callbacks.Logging = func(ctx context.Context, params *mcp.LoggingMessageParams) { //nolint:staticcheck // Required to proxy logging for legacy MCP versions.
		if frontend != nil && params != nil {
			_ = frontend.Log(ctx, params) //nolint:staticcheck // Required to proxy logging for legacy MCP versions.
		}
	}
	callbacks.ListChanged = func(_ context.Context, family component.FeatureFamily) {
		c.registry.RequestRefresh(fingerprint, func(success bool) {
			if fingerprint == c.defaultCatalogFingerprint {
				c.catalogDegraded.Store(!success)
			}
			if !success {
				return
			}
			for _, server := range c.bindings.matching(fingerprint, componentName) {
				notifyFeatureChanged(server, family)
			}
		})
	}
	callbacks.ResourceUpdate = func(ctx context.Context, uri string) {
		compiled, _, err := c.registry.Get(ctx, cfg)
		if err != nil {
			return
		}
		params := &mcp.ResourceUpdatedNotificationParams{URI: compiled.CompositeURI(prefix, uri)}
		for _, server := range c.bindings.matching(fingerprint, componentName) {
			_ = server.ResourceUpdated(ctx, params)
		}
	}
	return callbacks
}

func notifyFeatureChanged(server *mcp.Server, family component.FeatureFamily) {
	switch family {
	case component.FeatureTools:
		const name = "mmmcp_refresh_notification"
		server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, nil)
		server.RemoveTools(name)
	case component.FeaturePrompts:
		const name = "mmmcp_refresh_notification"
		server.AddPrompt(&mcp.Prompt{Name: name}, nil)
		server.RemovePrompts(name)
	case component.FeatureResources:
		const uri = "mmmcp-refresh://notification"
		server.AddResource(&mcp.Resource{URI: uri, Name: "mmmcp refresh notification"}, nil)
		server.RemoveResources(uri)
	}
}
