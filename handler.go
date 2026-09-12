package mmmcp

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

func (c *Composite) featureMiddleware(server *mcp.Server) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		// Each frontend server serves one session; retain its last config snapshot.
		var (
			mu                  sync.Mutex
			previous            *catalog.Catalog
			previousFingerprint string
		)
		handler := func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			compiled, fingerprint, err := c.catalogForRequest(ctx, request)
			if err != nil {
				return nil, err
			}
			if id, ok := ConfigIDFromContext(ctx); ok && request.GetSession().ID() == "" {
				if cleanup := c.toolSubscriptions.observe(id, method, server, compiled, fingerprint); cleanup != nil {
					defer cleanup()
				}
			} else {
				mu.Lock()
				toolsChanged := previous != nil && previousFingerprint != fingerprint && !reflect.DeepEqual(previous.Tools(), compiled.Tools())
				previous, previousFingerprint = compiled, fingerprint
				mu.Unlock()
				if toolsChanged && method != "tools/list" {
					notifyFeatureChanged(server, component.FeatureTools)
				}
			}
			switch method {
			case "tools/list":
				req, ok := request.(*mcp.ListToolsRequest)
				if !ok || req.Params == nil {
					return nil, invalidRequest(method)
				}
				values, nextCursor, err := compiled.PageTools(req.Params.Cursor, c.pageSize)
				if err != nil {
					return nil, err
				}
				return &mcp.ListToolsResult{CacheScope: "public", Tools: values, NextCursor: nextCursor}, nil
			case "prompts/list":
				req, ok := request.(*mcp.ListPromptsRequest)
				if !ok || req.Params == nil {
					return nil, invalidRequest(method)
				}
				values, nextCursor, err := compiled.PagePrompts(req.Params.Cursor, c.pageSize)
				if err != nil {
					return nil, err
				}
				return &mcp.ListPromptsResult{CacheScope: "public", Prompts: values, NextCursor: nextCursor}, nil
			case "resources/list":
				req, ok := request.(*mcp.ListResourcesRequest)
				if !ok || req.Params == nil {
					return nil, invalidRequest(method)
				}
				values, nextCursor, err := compiled.PageResources(req.Params.Cursor, c.pageSize)
				if err != nil {
					return nil, err
				}
				return &mcp.ListResourcesResult{CacheScope: "public", Resources: values, NextCursor: nextCursor}, nil
			case "resources/templates/list":
				req, ok := request.(*mcp.ListResourceTemplatesRequest)
				if !ok || req.Params == nil {
					return nil, invalidRequest(method)
				}
				values, nextCursor, err := compiled.PageResourceTemplates(req.Params.Cursor, c.pageSize)
				if err != nil {
					return nil, err
				}
				return &mcp.ListResourceTemplatesResult{CacheScope: "public", ResourceTemplates: values, NextCursor: nextCursor}, nil
			case "tools/call":
				return c.callTool(ctx, request, compiled, fingerprint)
			case "prompts/get":
				return c.getPrompt(ctx, request, compiled, fingerprint)
			case "resources/read":
				return c.readResource(ctx, request, compiled, fingerprint)
			default:
				return next(ctx, method, request)
			}
		}
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			result, err := handler(ctx, method, request)
			c.captureAuthorizationError(ctx, request, err)
			return result, err
		}
	}
}

func (c *Composite) catalogForRequest(ctx context.Context, request mcp.Request) (*catalog.Catalog, string, error) {
	return c.registry.Get(ctx, c.configForRequest(ctx, request))
}

func (c *Composite) configForRequest(ctx context.Context, request mcp.Request) *config.Config {
	if extra := request.GetExtra(); extra != nil && extra.Header != nil {
		if selection, ok := c.requestConfigs.get(extra.Header.Get(privateConfigHeader)); ok {
			if selection.useDefault {
				return c.defaultConfig
			}
			return selection.config
		}
	}
	return effectiveConfig(ctx, c.defaultConfig)
}

func (c *Composite) callTool(ctx context.Context, request mcp.Request, compiled *catalog.Catalog, fingerprint string) (mcp.Result, error) {
	req, ok := request.(*mcp.CallToolRequest)
	if !ok || req.Params == nil {
		return nil, invalidRequest("tools/call")
	}
	route, ok := compiled.RouteTool(req.Params.Name)
	if !ok {
		return nil, unknown("tool", req.Params.Name)
	}
	ctx = component.ContextWithToolCall(ctx, route.Tool, req.Params.Arguments)
	params := &mcp.CallToolParams{
		Meta:           component.DownstreamMeta(req.Params.Meta),
		Name:           route.Tool.Name,
		Arguments:      req.Params.Arguments,
		InputResponses: req.Params.InputResponses,
		RequestState:   req.Params.RequestState,
	}
	result, err := invoke(ctx, c, request, fingerprint, route.Component,
		func(invoker component.Invoker) (*mcp.CallToolResult, error) {
			return invoker.CallTool(ctx, route.Component, params)
		},
		func(runtime component.Runtime) (*mcp.CallToolResult, error) {
			return runtime.CallTool(ctx, params)
		}, route.Prefix)
	if err != nil {
		return nil, err
	}
	return normalizeCallToolResult(request, compiled.RewriteCallToolResult(route.Prefix, result))
}

func (c *Composite) getPrompt(ctx context.Context, request mcp.Request, compiled *catalog.Catalog, fingerprint string) (mcp.Result, error) {
	req, ok := request.(*mcp.GetPromptRequest)
	if !ok || req.Params == nil {
		return nil, invalidRequest("prompts/get")
	}
	route, ok := compiled.RoutePrompt(req.Params.Name)
	if !ok {
		return nil, unknown("prompt", req.Params.Name)
	}
	params := *req.Params
	params.Name = route.OriginalName
	params.Meta = component.DownstreamMeta(params.Meta)
	result, err := invoke(ctx, c, request, fingerprint, route.Component,
		func(invoker component.Invoker) (*mcp.GetPromptResult, error) {
			return invoker.GetPrompt(ctx, route.Component, &params)
		},
		func(runtime component.Runtime) (*mcp.GetPromptResult, error) {
			return runtime.GetPrompt(ctx, &params)
		}, route.Prefix)
	if err != nil {
		return nil, err
	}
	return normalizeGetPromptResult(request, compiled.RewriteGetPromptResult(route.Prefix, result))
}

func (c *Composite) readResource(ctx context.Context, request mcp.Request, compiled *catalog.Catalog, fingerprint string) (mcp.Result, error) {
	req, ok := request.(*mcp.ReadResourceRequest)
	if !ok || req.Params == nil {
		return nil, invalidRequest("resources/read")
	}
	route, ok := compiled.RouteResource(req.Params.URI)
	if !ok {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
	params := *req.Params
	params.URI = route.OriginalURI
	params.Meta = component.DownstreamMeta(params.Meta)
	result, err := invoke(ctx, c, request, fingerprint, route.Component,
		func(invoker component.Invoker) (*mcp.ReadResourceResult, error) {
			return invoker.ReadResource(ctx, route.Component, &params)
		},
		func(runtime component.Runtime) (*mcp.ReadResourceResult, error) {
			return runtime.ReadResource(ctx, &params)
		}, route.Prefix)
	if err != nil {
		return nil, err
	}
	return normalizeReadResourceResult(request, compiled.RewriteReadResourceResult(route.Prefix, result))
}

func (c *Composite) subscribe(ctx context.Context, req *mcp.SubscribeRequest) (err error) {
	defer func() { c.captureAuthorizationError(ctx, req, err) }()
	if req == nil || req.Params == nil {
		return invalidRequest("resources/subscribe")
	}
	compiled, fingerprint, err := c.catalogForRequest(ctx, req)
	if err != nil {
		return err
	}
	route, ok := compiled.RouteResource(req.Params.URI)
	if !ok {
		return mcp.ResourceNotFoundError(req.Params.URI)
	}
	params := *req.Params
	params.URI = route.OriginalURI
	params.Meta = component.DownstreamMeta(params.Meta)
	if req.Session != nil && req.Session.ID() != "" {
		cfg := c.configForRequest(ctx, req)
		runtime, release, err := c.pool.Acquire(ctx, req.Session, fingerprint, route.Component, c.runtimeOptions(req.Session, cfg, fingerprint, route.Component, route.Prefix, 0))
		if err != nil {
			return err
		}
		defer release()
		return runtime.Subscribe(ctx, &params)
	}
	return c.factory.Subscribe(ctx, route.Component, &params)
}

func (c *Composite) unsubscribe(ctx context.Context, req *mcp.UnsubscribeRequest) (err error) {
	defer func() { c.captureAuthorizationError(ctx, req, err) }()
	if req == nil || req.Params == nil {
		return invalidRequest("resources/unsubscribe")
	}
	compiled, fingerprint, err := c.catalogForRequest(ctx, req)
	if err != nil {
		return err
	}
	route, ok := compiled.RouteResource(req.Params.URI)
	if !ok {
		return mcp.ResourceNotFoundError(req.Params.URI)
	}
	params := *req.Params
	params.URI = route.OriginalURI
	params.Meta = component.DownstreamMeta(params.Meta)
	if req.Session != nil && req.Session.ID() != "" {
		cfg := c.configForRequest(ctx, req)
		runtime, release, err := c.pool.Acquire(ctx, req.Session, fingerprint, route.Component, c.runtimeOptions(req.Session, cfg, fingerprint, route.Component, route.Prefix, 0))
		if err != nil {
			return err
		}
		defer release()
		return runtime.Unsubscribe(ctx, &params)
	}
	return c.factory.Unsubscribe(ctx, route.Component, &params)
}

func invoke[T any](ctx context.Context, c *Composite, request mcp.Request, fingerprint string, server config.Server, stateless func(component.Invoker) (T, error), stateful func(component.Runtime) (T, error), prefix string) (T, error) {
	if frontend, ok := request.GetSession().(*mcp.ServerSession); ok && frontend.ID() != "" {
		frontendActivity := c.bindFrontendActivity(frontend)
		cfg := c.configForRequest(ctx, request)
		runtime, release, err := c.pool.Acquire(ctx, frontend, fingerprint, server, c.runtimeOptions(frontend, cfg, fingerprint, server, prefix, frontendActivity))
		if err != nil {
			var zero T
			return zero, err
		}
		defer release()
		return stateful(runtime)
	}
	return stateless(c.factory)
}

func (c *Composite) runtimeOptions(frontend *mcp.ServerSession, cfg *config.Config, fingerprint string, server config.Server, prefix string, frontendActivity int) component.RuntimeOptions {
	return component.RuntimeOptions{
		IdleTimeout:      cfg.IdleTimeout,
		FrontendActivity: frontendActivity,
		Callbacks:        c.callbacksForComponent(frontend, cfg, fingerprint, server.Name, prefix),
		OnOpen: func() {
			c.bindings.bind(frontend, fingerprint, server.Name)
		},
		OnClose: func() {
			c.bindings.unbind(frontend, fingerprint, server.Name)
		},
	}
}

func invalidRequest(method string) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid " + method + " request"}
}

func unknown(family, identity string) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("unknown %s %q", family, identity)}
}
