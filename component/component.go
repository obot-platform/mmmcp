// Package component defines the downstream MCP boundary used by the catalog
// and composite server.
package component

import (
	"context"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/config"
)

const (
	FeatureTools     FeatureFamily = "tools"
	FeaturePrompts   FeatureFamily = "prompts"
	FeatureResources FeatureFamily = "resources"
)

// Features is a complete component discovery snapshot.
type Features struct {
	Tools             []*mcp.Tool
	Prompts           []*mcp.Prompt
	Resources         []*mcp.Resource
	ResourceTemplates []*mcp.ResourceTemplate
}

// Discoverer discovers every supported feature exposed by a component.
type Discoverer interface {
	// Discover may be called concurrently and must be safe for concurrent use.
	Discover(context.Context, config.Server) (*Features, error)
}

// Invoker invokes component operations in request-scoped runtimes.
type Invoker interface {
	CallTool(context.Context, config.Server, *mcp.CallToolParams) (*mcp.CallToolResult, error)
	GetPrompt(context.Context, config.Server, *mcp.GetPromptParams) (*mcp.GetPromptResult, error)
	ReadResource(context.Context, config.Server, *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error)
	Subscribe(context.Context, config.Server, *mcp.SubscribeParams) error
	Unsubscribe(context.Context, config.Server, *mcp.UnsubscribeParams) error
}

// Runtime is one connected component session used for multiple operations.
type Runtime interface {
	CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error)
	GetPrompt(context.Context, *mcp.GetPromptParams) (*mcp.GetPromptResult, error)
	ReadResource(context.Context, *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error)
	Subscribe(context.Context, *mcp.SubscribeParams) error
	Unsubscribe(context.Context, *mcp.UnsubscribeParams) error
	Close() error
}

// RuntimeOptions controls the lifecycle of a stateful component runtime.
type RuntimeOptions struct {
	IdleTimeout      time.Duration
	FrontendActivity int
	Callbacks        Callbacks
	OnOpen           func()
	OnClose          func()
}

// FeatureFamily identifies a composite catalog family changed by a component.
type FeatureFamily string

// Callbacks binds one downstream session to its exact frontend session and
// configuration. A zero Frontend marks a stateless runtime, where direct
// server-to-client requests are rejected.
type Callbacks struct {
	Frontend       *mcp.ServerSession
	Component      string
	Progress       func(context.Context, *mcp.ProgressNotificationParams)
	Logging        func(context.Context, *mcp.LoggingMessageParams) //nolint:staticcheck // Required to proxy logging for legacy MCP versions.
	ListChanged    func(context.Context, FeatureFamily)
	ResourceUpdate func(context.Context, string)
}

type requestHeadersContextKey struct{}

type valueSuppressingContext struct {
	context.Context
}

// FrontendActivityRuntime receives the number of active frontend HTTP
// requests associated with its stateful session.
type FrontendActivityRuntime interface {
	SetFrontendActivity(int)
}

// RuntimeFactory opens connected component sessions for stateful frontends.
type RuntimeFactory interface {
	OpenRuntime(context.Context, config.Server, RuntimeOptions) (Runtime, error)
}

// Factory provides discovery and invocation operations.
type Factory interface {
	Discoverer
	Invoker
	RuntimeFactory
	Close() error
}

// WithoutValues preserves cancellation and deadlines while preventing private
// values owned by an upstream SDK session from leaking into a downstream one.
func WithoutValues(ctx context.Context) context.Context {
	return valueSuppressingContext{Context: ctx}
}

// ContextWithRequestHeaders attaches an immutable snapshot of the headers from
// the frontend HTTP request. The snapshot is the only context value preserved
// by WithoutValues for downstream HTTP forwarding.
func ContextWithRequestHeaders(ctx context.Context, headers http.Header) context.Context {
	return context.WithValue(ctx, requestHeadersContextKey{}, headers.Clone())
}

// RequestHeadersFromContext returns a copy of the frontend request headers.
func RequestHeadersFromContext(ctx context.Context) http.Header {
	headers, _ := ctx.Value(requestHeadersContextKey{}).(http.Header)
	return headers.Clone()
}

func (c valueSuppressingContext) Value(key any) any {
	if _, ok := key.(requestHeadersContextKey); ok {
		return c.Context.Value(key)
	}
	return nil
}

// DownstreamMeta removes transport-owned frontend metadata while preserving
// application metadata forwarded to a downstream component. The downstream
// client session injects values appropriate for its negotiated protocol.
func DownstreamMeta(meta mcp.Meta) mcp.Meta {
	_, hasVersion := meta[mcp.MetaKeyProtocolVersion]
	_, hasInfo := meta[mcp.MetaKeyClientInfo]
	_, hasCapabilities := meta[mcp.MetaKeyClientCapabilities]
	if !hasVersion && !hasInfo && !hasCapabilities {
		return meta
	}
	clean := make(mcp.Meta, len(meta))
	for key, value := range meta {
		switch key {
		case mcp.MetaKeyProtocolVersion, mcp.MetaKeyClientInfo, mcp.MetaKeyClientCapabilities:
		default:
			clean[key] = value
		}
	}
	return clean
}
