package mmmcp

import (
	"context"
	"errors"
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
	toolSearch  bool
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
	watched  map[*mcp.ServerSession]struct{}
	wg       sync.WaitGroup
}

// observe retains snapshots only while a configuration has active listeners.
func (s *configToolSubscriptions) observe(id, method string, server *mcp.Server, compiled *catalog.Catalog, fingerprint string, toolSearch bool) func() {
	s.mu.Lock()
	entry := s.configs[id]
	var notify, setup []*mcp.Server
	if entry != nil {
		setupDone := setupChanged(entry.catalog, compiled)
		if (entry.fingerprint != fingerprint || setupDone) && (entry.toolSearch != toolSearch || !reflect.DeepEqual(entry.catalog.Tools(), compiled.Tools())) {
			for listener := range entry.servers {
				notify = append(notify, listener)
			}
		}
		if setupDone {
			for listener := range entry.servers {
				setup = append(setup, listener)
			}
		}
		entry.catalog, entry.fingerprint, entry.toolSearch = compiled, fingerprint, toolSearch
	}
	var cleanup func()
	if method == subscriptionsListenMethod {
		if entry == nil {
			entry = &configToolSubscription{
				catalog:     compiled,
				fingerprint: fingerprint,
				toolSearch:  toolSearch,
				servers:     make(map[*mcp.Server]struct{}),
			}
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
	for _, listener := range setup {
		notifySetupChanged(listener)
	}
	return cleanup
}

func (b *frontendBindings) bindServer(session *mcp.ServerSession, server *mcp.Server) {
	if session == nil || server == nil {
		return
	}

	// Sessions without IDs do not use persistent component runtimes.
	if session.ID() == "" {
		return
	}

	b.mu.Lock()
	if b.servers == nil {
		b.servers = make(map[*mcp.ServerSession]*mcp.Server)
	}
	b.servers[session] = server

	// Watch the session lifetime even when its last component runtime unbinds.
	_, watched := b.watched[session]
	if !watched {
		if b.watched == nil {
			b.watched = make(map[*mcp.ServerSession]struct{})
		}
		b.watched[session] = struct{}{}
		b.wg.Add(1)
	}
	b.mu.Unlock()

	if !watched {
		go func() {
			defer b.wg.Done()

			// Wait outside the lock so live sessions can keep updating bindings.
			_ = session.Wait()

			b.mu.Lock()
			delete(b.servers, session)
			delete(b.sessions, session)
			delete(b.watched, session)
			b.mu.Unlock()
		}()
	}
}

// close runs after HTTP admission stops and all admitted requests return.
func (b *frontendBindings) close() error {
	b.mu.Lock()
	sessions := make([]*mcp.ServerSession, 0, len(b.watched))
	for session := range b.watched {
		sessions = append(sessions, session)
	}
	b.mu.Unlock()

	var result error
	for _, session := range sessions {
		result = errors.Join(result, session.Close())
	}
	b.wg.Wait()

	return result
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

			// Legacy HTTP sessions detach request cancellation in the SDK.
			ctx, cancel := context.WithCancel(ctx)
			stop := context.AfterFunc(c.shutdownCtx, cancel)
			defer func() {
				stop()
				cancel()
			}()

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
		if cfg.RequestScopedDiscovery {
			c.registry.InvalidateComponent(componentName)
			for _, server := range c.bindings.matching(fingerprint, componentName) {
				notifyFeatureChanged(server, family)
			}

			return
		}

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
		resourceConfig := c.requestCatalogConfig(cfg, "resources/list", "", nil)
		compiled, _, err := c.registry.Get(ctx, resourceConfig)
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
