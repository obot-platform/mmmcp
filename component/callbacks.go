package component

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewClient creates a downstream client whose callbacks are bound to one
// frontend session. Multi-round-trip handling is deliberately disabled so
// continuation results can cross the composite boundary unchanged.
func NewClient(implementation *mcp.Implementation, callbacks Callbacks) *mcp.Client {
	if implementation == nil {
		implementation = &mcp.Implementation{Name: "mmmcp", Version: "dev"}
	}
	options := &mcp.ClientOptions{
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: callbacks.Frontend == nil},
	}
	if callbacks.Progress != nil {
		options.ProgressNotificationHandler = func(ctx context.Context, req *mcp.ProgressNotificationClientRequest) {
			if callbacks.Progress != nil && req != nil {
				callbacks.Progress(ctx, req.Params)
			}
		}
	}
	if callbacks.Logging != nil {
		options.LoggingMessageHandler = func(ctx context.Context, req *mcp.LoggingMessageRequest) { //nolint:staticcheck // Required to proxy logging for legacy MCP versions.
			if callbacks.Logging != nil && req != nil {
				callbacks.Logging(ctx, req.Params)
			}
		}
	}
	if callbacks.ListChanged != nil {
		options.ToolListChangedHandler = func(ctx context.Context, _ *mcp.ToolListChangedRequest) { callbacks.ListChanged(ctx, FeatureTools) }
		options.PromptListChangedHandler = func(ctx context.Context, _ *mcp.PromptListChangedRequest) { callbacks.ListChanged(ctx, FeaturePrompts) }
		options.ResourceListChangedHandler = func(ctx context.Context, _ *mcp.ResourceListChangedRequest) {
			callbacks.ListChanged(ctx, FeatureResources)
		}
	}
	if callbacks.ResourceUpdate != nil {
		options.ResourceUpdatedHandler = func(ctx context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
			if callbacks.ResourceUpdate != nil && req != nil && req.Params != nil {
				callbacks.ResourceUpdate(ctx, req.Params.URI)
			}
		}
	}
	if callbacks.Frontend != nil {
		options.CreateMessageWithToolsHandler = func(ctx context.Context, req *mcp.CreateMessageWithToolsRequest) (*mcp.CreateMessageWithToolsResult, error) { //nolint:staticcheck // Required to proxy sampling for legacy MCP versions.
			return callbacks.Frontend.CreateMessageWithTools(ctx, req.Params) //nolint:staticcheck // Required to proxy sampling for legacy MCP versions.
		}
		options.ElicitationHandler = func(ctx context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return callbacks.Frontend.Elicit(ctx, req.Params)
		}
	}

	client := mcp.NewClient(implementation, options)
	client.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if callbacks.Frontend == nil {
				switch method {
				case "roots/list", "sampling/createMessage", "elicitation/create":
					return nil, unsupportedDirectCallback(callbacks.Component, method)
				}
			}
			if method != "roots/list" {
				return next(ctx, method, req)
			}
			roots, ok := req.(*mcp.ListRootsRequest)
			if !ok {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid roots/list request"}
			}
			return callbacks.Frontend.ListRoots(ctx, roots.Params) //nolint:staticcheck // Required to proxy roots for legacy MCP versions.
		}
	})
	return client
}

func unsupportedDirectCallback(componentName, method string) error {
	return &jsonrpc.Error{
		Code:    jsonrpc.CodeMethodNotFound,
		Message: fmt.Sprintf("component %q cannot use direct %s during a stateless modern call; return an MCP multi-round-trip input request instead", componentName, method),
	}
}
