package stdio

import (
	"context"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

var (
	implementation = &mcp.Implementation{Name: "mmmcp", Version: "dev"}
)

type runtimeSession struct {
	server  config.Server
	session *mcp.ClientSession
	once    sync.Once
	err     error
}

func connect(ctx context.Context, server config.Server, builder commandBuilder, callbacks component.Callbacks) (*runtimeSession, error) {
	ctx, cancel := withTimeout(ctx, server.Timeout)
	defer cancel()
	ctx = component.WithoutValues(ctx)
	client := component.NewClient(implementation, callbacks)
	session, err := client.Connect(ctx, builder.transport(server), nil)
	if err != nil {
		return nil, err
	}
	return &runtimeSession{server: server, session: session}, nil
}

func (r *runtimeSession) CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	ctx, cancel := withTimeout(ctx, r.server.Timeout)
	defer cancel()
	return r.session.CallTool(component.WithoutValues(ctx), params)
}

func (r *runtimeSession) GetPrompt(ctx context.Context, params *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
	ctx, cancel := withTimeout(ctx, r.server.Timeout)
	defer cancel()
	return r.session.GetPrompt(component.WithoutValues(ctx), params)
}

func (r *runtimeSession) ReadResource(ctx context.Context, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
	ctx, cancel := withTimeout(ctx, r.server.Timeout)
	defer cancel()
	return r.session.ReadResource(component.WithoutValues(ctx), params)
}

func (r *runtimeSession) Subscribe(ctx context.Context, params *mcp.SubscribeParams) error {
	ctx, cancel := withTimeout(ctx, r.server.Timeout)
	defer cancel()
	return r.session.Subscribe(component.WithoutValues(ctx), params)
}

func (r *runtimeSession) Unsubscribe(ctx context.Context, params *mcp.UnsubscribeParams) error {
	ctx, cancel := withTimeout(ctx, r.server.Timeout)
	defer cancel()
	return r.session.Unsubscribe(component.WithoutValues(ctx), params)
}

func (r *runtimeSession) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(func() { r.err = r.session.Close() })
	return r.err
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}
