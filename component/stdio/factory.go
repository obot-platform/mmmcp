package stdio

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

// Options controls command construction and shutdown.
type Options struct {
	Logger            *slog.Logger
	LookupEnv         func(string) (string, bool)
	TerminateDuration time.Duration
	ClientInfo        *mcp.Implementation
}

// Factory creates isolated command-backed component sessions.
type Factory struct {
	builder    commandBuilder
	clientInfo *mcp.Implementation
	clock      clock
	mu         sync.Mutex
	closing    bool
	cleanup    sync.WaitGroup
}

// NewFactory creates a stdio component factory.
func NewFactory(opts Options) *Factory {
	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return &Factory{builder: commandBuilder{
		lookupEnv:         lookup,
		logger:            opts.Logger,
		terminateDuration: opts.TerminateDuration,
	}, clientInfo: opts.ClientInfo, clock: realClock{}}
}

func (f *Factory) open(ctx context.Context, server config.Server, callbacks component.Callbacks) (*runtimeSession, error) {
	if server.Command == "" {
		return nil, fmt.Errorf("component %q is not configured for stdio", server.Name)
	}
	runtime, err := connect(ctx, server, f.builder, f.clientInfo, callbacks)
	if err != nil {
		return nil, fmt.Errorf("component %q connect: %w", server.Name, err)
	}
	return runtime, nil
}

// Discover starts an isolated process and exhausts all feature lists.
func (f *Factory) Discover(ctx context.Context, server config.Server) (*component.Features, error) {
	ctx, cancel := withTimeout(ctx, server.Timeout)
	defer cancel()
	runtime, err := f.open(ctx, server, component.Callbacks{})
	if err != nil {
		return nil, err
	}
	defer runtime.Close()
	features := new(component.Features)
	if initialized := runtime.session.InitializeResult(); initialized != nil && initialized.ServerInfo != nil {
		features.ServerInfo = &mcp.Implementation{
			Name:    initialized.ServerInfo.Name,
			Version: initialized.ServerInfo.Version,
		}
	}
	if err := paginate(server.Name, "tools/list", func(cursor string) (string, error) {
		result, err := runtime.session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err == nil {
			features.Tools = append(features.Tools, result.Tools...)
			return result.NextCursor, nil
		}
		return "", err
	}); err != nil {
		return nil, err
	}
	if err := paginate(server.Name, "prompts/list", func(cursor string) (string, error) {
		result, err := runtime.session.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: cursor})
		if err == nil {
			features.Prompts = append(features.Prompts, result.Prompts...)
			return result.NextCursor, nil
		}
		return "", err
	}); err != nil {
		return nil, err
	}
	if err := paginate(server.Name, "resources/list", func(cursor string) (string, error) {
		result, err := runtime.session.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err == nil {
			features.Resources = append(features.Resources, result.Resources...)
			return result.NextCursor, nil
		}
		return "", err
	}); err != nil {
		return nil, err
	}
	if err := paginate(server.Name, "resources/templates/list", func(cursor string) (string, error) {
		result, err := runtime.session.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{Cursor: cursor})
		if err == nil {
			features.ResourceTemplates = append(features.ResourceTemplates, result.ResourceTemplates...)
			return result.NextCursor, nil
		}
		return "", err
	}); err != nil {
		return nil, err
	}
	return features, nil
}

func withSession[T any](ctx context.Context, f *Factory, server config.Server, call func(*runtimeSession) (T, error)) (T, error) {
	var zero T
	if err := f.beginOneOff(); err != nil {
		return zero, err
	}
	runtime, err := f.open(ctx, server, component.Callbacks{Component: server.Name})
	if err != nil {
		f.cleanup.Done()
		return zero, err
	}
	result, callErr := call(runtime)
	if callErr != nil {
		closeErr := runtime.Close()
		f.cleanup.Done()
		return result, errors.Join(callErr, closeErr)
	}
	go func() {
		defer f.cleanup.Done()
		_ = runtime.Close()
	}()
	return result, nil
}

func (f *Factory) beginOneOff() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closing {
		return errors.New("stdio component factory is closed")
	}
	f.cleanup.Add(1)
	return nil
}

func (f *Factory) CallTool(ctx context.Context, server config.Server, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	return withSession(ctx, f, server, func(runtime *runtimeSession) (*mcp.CallToolResult, error) { return runtime.CallTool(ctx, params) })
}

func (f *Factory) GetPrompt(ctx context.Context, server config.Server, params *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
	return withSession(ctx, f, server, func(runtime *runtimeSession) (*mcp.GetPromptResult, error) { return runtime.GetPrompt(ctx, params) })
}

func (f *Factory) ReadResource(ctx context.Context, server config.Server, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
	return withSession(ctx, f, server, func(runtime *runtimeSession) (*mcp.ReadResourceResult, error) {
		return runtime.ReadResource(ctx, params)
	})
}

func (f *Factory) Subscribe(ctx context.Context, server config.Server, params *mcp.SubscribeParams) error {
	_, err := withSession(ctx, f, server, func(runtime *runtimeSession) (struct{}, error) { return struct{}{}, runtime.Subscribe(ctx, params) })
	return err
}

func (f *Factory) Unsubscribe(ctx context.Context, server config.Server, params *mcp.UnsubscribeParams) error {
	_, err := withSession(ctx, f, server, func(runtime *runtimeSession) (struct{}, error) { return struct{}{}, runtime.Unsubscribe(ctx, params) })
	return err
}

func (f *Factory) OpenRuntime(_ context.Context, server config.Server, opts component.RuntimeOptions) (component.Runtime, error) {
	if server.Command == "" {
		return nil, fmt.Errorf("component %q is not configured for stdio", server.Name)
	}
	manager := newManager(func(ctx context.Context) (component.Runtime, error) { return f.open(ctx, server, opts.Callbacks) }, opts.IdleTimeout, f.clock)
	manager.SetFrontendActivity(opts.FrontendActivity)
	return manager, nil
}

func (f *Factory) Close() error {
	f.mu.Lock()
	f.closing = true
	f.mu.Unlock()
	f.cleanup.Wait()
	return nil
}

func paginate(componentName, method string, page func(string) (string, error)) error {
	var cursor string
	seen := make(map[string]struct{})
	for {
		next, err := page(cursor)
		var rpcErr *jsonrpc.Error
		if errors.As(err, &rpcErr) && rpcErr.Code == jsonrpc.CodeMethodNotFound {
			return nil
		}
		if err != nil {
			return fmt.Errorf("component %q %s: %w", componentName, method, err)
		}
		if next == "" {
			return nil
		}
		if _, ok := seen[next]; ok {
			return fmt.Errorf("component %q %s returned repeated cursor %q", componentName, method, next)
		}
		seen[next] = struct{}{}
		cursor = next
	}
}
