// Package mmmcp combines configured MCP components behind one server.
package mmmcp

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	componenthttp "github.com/obot-platform/mmmcp/component/http"
	componentstdio "github.com/obot-platform/mmmcp/component/stdio"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/storage"
)

const (
	implementationName    = "mmmcp"
	implementationVersion = "dev"
)

// Composite is a reusable composite MCP server.
type Composite struct {
	defaultConfig             *config.Config
	registry                  *catalog.Registry
	factory                   component.Factory
	pool                      *component.Pool
	pageSize                  int
	handler                   http.Handler
	requestConfigs            requestConfigRegistry
	activity                  httpActivityRegistry
	bindings                  frontendBindings
	authorizationErrors       sync.Map
	stores                    *storage.Registry
	defaultStore              storage.Store
	events                    *storage.EventAdapter
	serverOptions             Options
	implementation            mcp.Implementation
	startedAt                 time.Time
	defaultCatalogFingerprint string
	catalogDegraded           atomic.Bool
	stdioMu                   sync.Mutex
	stdioCancels              map[uint64]context.CancelFunc
	stdioNext                 uint64
	stdioWG                   sync.WaitGroup
	closed                    atomic.Bool
}

// New discovers the configured components and creates a composite server.
func New(ctx context.Context, cfg *config.Config, opts Options) (*Composite, error) {
	if cfg == nil {
		return nil, errors.New("mmmcp: nil config")
	}
	stores := storage.NewRegistry(opts.Storage)
	defaultStore, err := stores.Get(ctx, opts.DSN)
	if err != nil {
		_ = stores.Close()
		return nil, err
	}
	httpFactory := componenthttp.NewFactory(componenthttp.FactoryOptions{
		HTTPClient: opts.HTTPClient,
		OAuth:      opts.OAuth,
	})
	stdioFactory := componentstdio.NewFactory(componentstdio.Options{
		Logger:            opts.Logger,
		LookupEnv:         opts.LookupEnv,
		TerminateDuration: opts.CommandTerminateDuration,
	})
	factory := component.NewRoutingFactory(httpFactory, stdioFactory)
	registry := catalog.NewRegistry(factory)
	defaultCatalog, defaultCatalogFingerprint, err := registry.Get(ctx, cfg)
	if err != nil {
		_ = factory.Close()
		_ = stores.Close()
		return nil, err
	}

	c := &Composite{
		defaultConfig:             cfg,
		registry:                  registry,
		factory:                   factory,
		pool:                      component.NewPool(factory),
		pageSize:                  opts.PageSize,
		stores:                    stores,
		defaultStore:              defaultStore,
		serverOptions:             opts,
		implementation:            frontendImplementation(cfg, defaultCatalog),
		startedAt:                 time.Now(),
		defaultCatalogFingerprint: defaultCatalogFingerprint,
		stdioCancels:              make(map[uint64]context.CancelFunc),
	}
	c.events = storage.NewEventAdapter(stores, func(ctx context.Context) string {
		return effectiveDSN(ctx, c.serverOptions.DSN)
	})
	c.handler = c.newHTTPHandler(opts)
	return c, nil
}

// HTTPHandler returns the Streamable HTTP MCP, health, and readiness handler.
func (c *Composite) HTTPHandler() http.Handler { return c.handler }

// Refresh recompiles the default configuration's immutable catalog.
func (c *Composite) Refresh(ctx context.Context) error {
	if c == nil || c.closed.Load() {
		return errors.New("mmmcp: composite server is closed")
	}
	_, _, err := c.registry.Refresh(ctx, c.defaultConfig)
	c.catalogDegraded.Store(err != nil)
	return err
}

// Close prevents new requests and releases component resources.
func (c *Composite) Close() error {
	if c == nil || !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	c.stdioMu.Lock()
	for _, cancel := range c.stdioCancels {
		cancel()
	}
	c.stdioMu.Unlock()
	c.stdioWG.Wait()
	c.registry.Close()
	return errors.Join(c.pool.Close(), c.factory.Close(), c.stores.Close())
}

func newFrontendServer(c *Composite, opts Options) *mcp.Server {
	implementation := c.implementation
	server := mcp.NewServer(&implementation, &mcp.ServerOptions{
		Logger: opts.Logger,
		Capabilities: &mcp.ServerCapabilities{
			Tools:     &mcp.ToolCapabilities{ListChanged: true},
			Prompts:   &mcp.PromptCapabilities{ListChanged: true},
			Resources: &mcp.ResourceCapabilities{Subscribe: true, ListChanged: true},
			Logging:   &mcp.LoggingCapabilities{}, //nolint:staticcheck // Advertise logging to supported legacy MCP clients.
		},
		SubscribeHandler:   c.subscribe,
		UnsubscribeHandler: c.unsubscribe,
	})
	server.AddReceivingMiddleware(c.bindFrontendServer(server), c.configMiddleware(), c.featureMiddleware())
	return server
}

func frontendImplementation(cfg *config.Config, discovered *catalog.Catalog) mcp.Implementation {
	implementation := mcp.Implementation{Name: implementationName, Version: implementationVersion}
	if len(cfg.Servers) == 1 {
		if serverInfo := discovered.ServerInfo(); serverInfo != nil {
			implementation.Name = serverInfo.Name
			implementation.Version = serverInfo.Version
		}
		return implementation
	}
	if cfg.Name != "" {
		implementation.Name = cfg.Name
	}
	if cfg.Version != "" {
		implementation.Version = cfg.Version
	}
	return implementation
}
