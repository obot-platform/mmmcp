// Package http implements Streamable HTTP component clients.
package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

// Factory creates isolated Streamable HTTP client sessions.
type Factory struct {
	HTTPClient *http.Client
	OAuth      OAuthHandlerProvider
	ClientInfo *mcp.Implementation
}

// FactoryOptions configures a Streamable HTTP component factory.
type FactoryOptions struct {
	HTTPClient *http.Client
	OAuth      OAuthHandlerProvider
	ClientInfo *mcp.Implementation
}

type runtime struct {
	server  config.Server
	session *mcp.ClientSession
}

type headerTransport struct {
	base               http.RoundTripper
	headers            map[string]string
	passthroughHeaders []string
}

type headerSchemaProperty struct {
	Header     json.RawMessage                 `json:"x-mcp-header"`
	Properties map[string]headerSchemaProperty `json:"properties"`
}

// NewFactory creates a Streamable HTTP component factory.
func NewFactory(opts FactoryOptions) *Factory {
	return &Factory{HTTPClient: opts.HTTPClient, OAuth: opts.OAuth, ClientInfo: opts.ClientInfo}
}

// Discover connects to a component and exhausts every feature list.
func (f *Factory) Discover(ctx context.Context, server config.Server) (*component.Features, error) {
	ctx, cancel := withTimeout(ctx, server.Timeout)
	defer cancel()
	ctx = component.WithoutValues(ctx)

	session, err := f.connect(ctx, server, component.Callbacks{})
	if err != nil {
		return nil, fmt.Errorf("component %q discovery: %w", server.Name, err)
	}
	defer session.Close()

	features := new(component.Features)
	if initialized := session.InitializeResult(); initialized != nil && initialized.ServerInfo != nil {
		features.ServerInfo = &mcp.Implementation{
			Name:    initialized.ServerInfo.Name,
			Version: initialized.ServerInfo.Version,
		}
	}
	if err := paginate(server.Name, "tools/list", func(cursor string) (string, error) {
		result, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return "", err
		}
		features.Tools = append(features.Tools, result.Tools...)
		return result.NextCursor, nil
	}); err != nil {
		return nil, err
	}
	if err := paginate(server.Name, "prompts/list", func(cursor string) (string, error) {
		result, err := session.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: cursor})
		if err != nil {
			return "", err
		}
		features.Prompts = append(features.Prompts, result.Prompts...)
		return result.NextCursor, nil
	}); err != nil {
		return nil, err
	}
	if err := paginate(server.Name, "resources/list", func(cursor string) (string, error) {
		result, err := session.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return "", err
		}
		features.Resources = append(features.Resources, result.Resources...)
		return result.NextCursor, nil
	}); err != nil {
		return nil, err
	}
	if err := paginate(server.Name, "resources/templates/list", func(cursor string) (string, error) {
		result, err := session.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{Cursor: cursor})
		if err != nil {
			return "", err
		}
		features.ResourceTemplates = append(features.ResourceTemplates, result.ResourceTemplates...)
		return result.NextCursor, nil
	}); err != nil {
		return nil, err
	}
	return features, nil
}

// CallTool creates a fresh component session, invokes one tool, and closes it.
func (f *Factory) CallTool(ctx context.Context, server config.Server, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	params = downstreamCallToolParams(params)
	return withSession(ctx, f, server, "call", func(ctx context.Context, session *mcp.ClientSession) (*mcp.CallToolResult, error) {
		return session.CallTool(ctx, params)
	})
}

// GetPrompt gets one component prompt in a fresh component session.
func (f *Factory) GetPrompt(ctx context.Context, server config.Server, params *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
	params = downstreamGetPromptParams(params)
	return withSession(ctx, f, server, "prompt", func(ctx context.Context, session *mcp.ClientSession) (*mcp.GetPromptResult, error) {
		return session.GetPrompt(ctx, params)
	})
}

// ReadResource reads one component resource in a fresh component session.
func (f *Factory) ReadResource(ctx context.Context, server config.Server, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
	params = downstreamReadResourceParams(params)
	return withSession(ctx, f, server, "resource", func(ctx context.Context, session *mcp.ClientSession) (*mcp.ReadResourceResult, error) {
		return session.ReadResource(ctx, params)
	})
}

// Subscribe forwards a resource subscription in a fresh component session.
func (f *Factory) Subscribe(ctx context.Context, server config.Server, params *mcp.SubscribeParams) error {
	_, err := withSession(ctx, f, server, "subscribe", func(ctx context.Context, session *mcp.ClientSession) (struct{}, error) {
		return struct{}{}, session.Subscribe(ctx, params)
	})
	return err
}

// Unsubscribe forwards a resource unsubscription in a fresh component session.
func (f *Factory) Unsubscribe(ctx context.Context, server config.Server, params *mcp.UnsubscribeParams) error {
	_, err := withSession(ctx, f, server, "unsubscribe", func(ctx context.Context, session *mcp.ClientSession) (struct{}, error) {
		return struct{}{}, session.Unsubscribe(ctx, params)
	})
	return err
}

// OpenRuntime opens one persistent downstream client session.
func (f *Factory) OpenRuntime(ctx context.Context, server config.Server, opts component.RuntimeOptions) (component.Runtime, error) {
	ctx, cancel := withTimeout(ctx, server.Timeout)
	defer cancel()
	ctx = component.WithoutValues(ctx)
	session, err := f.connectWithStandaloneSSE(ctx, server, true, opts.Callbacks)
	if err != nil {
		return nil, fmt.Errorf("component %q connect: %w", server.Name, err)
	}
	return &runtime{server: server, session: session}, nil
}

func withSession[T any](ctx context.Context, f *Factory, server config.Server, operation string, call func(context.Context, *mcp.ClientSession) (T, error)) (T, error) {
	var zero T
	ctx, cancel := withTimeout(ctx, server.Timeout)
	defer cancel()
	ctx = component.WithoutValues(ctx)

	session, err := f.connect(ctx, server, component.Callbacks{Component: server.Name})
	if err != nil {
		return zero, fmt.Errorf("component %q %s: %w", server.Name, operation, err)
	}
	defer session.Close()
	return call(ctx, session)
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

// Close releases factory-owned resources. HTTP sessions are request-scoped.
func (*Factory) Close() error { return nil }

func (f *Factory) connect(ctx context.Context, server config.Server, callbacks component.Callbacks) (*mcp.ClientSession, error) {
	return f.connectWithStandaloneSSE(ctx, server, false, callbacks)
}

func (f *Factory) connectWithStandaloneSSE(ctx context.Context, server config.Server, standaloneSSE bool, callbacks component.Callbacks) (*mcp.ClientSession, error) {
	if server.URL == "" {
		return nil, fmt.Errorf("component is not configured for HTTP")
	}
	var oauthHandler auth.OAuthHandler
	if f.OAuth != nil {
		var err error
		oauthHandler, err = f.OAuth.OAuthHandler(ctx, server)
		if err != nil {
			return nil, fmt.Errorf("OAuth handler: %w", err)
		}
		if oauthHandler != nil {
			oauthHandler = typedOAuthHandler{OAuthHandler: oauthHandler}
		}
	}
	if oauthHandler == nil {
		oauthHandler = authorizationErrorHandler{}
	}
	clientInfo := f.ClientInfo
	if forwarded := component.ClientInfoFromContext(ctx); forwarded != nil {
		clientInfo = forwarded
	}
	client := component.NewClient(clientInfo, callbacks)
	transport := &mcp.StreamableClientTransport{
		Endpoint:             server.URL,
		HTTPClient:           clientWithHeaders(f.HTTPClient, server.Headers, server.PassthroughHeaders),
		DisableStandaloneSSE: !standaloneSSE,
		OAuthHandler:         oauthHandler,
	}
	return client.Connect(ctx, transport, nil)
}
func (r *runtime) CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	ctx, cancel := withTimeout(ctx, r.server.Timeout)
	defer cancel()
	return r.session.CallTool(component.WithoutValues(ctx), downstreamCallToolParams(params))
}

func (r *runtime) GetPrompt(ctx context.Context, params *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
	ctx, cancel := withTimeout(ctx, r.server.Timeout)
	defer cancel()
	return r.session.GetPrompt(component.WithoutValues(ctx), downstreamGetPromptParams(params))
}

func (r *runtime) ReadResource(ctx context.Context, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
	ctx, cancel := withTimeout(ctx, r.server.Timeout)
	defer cancel()
	return r.session.ReadResource(component.WithoutValues(ctx), downstreamReadResourceParams(params))
}

func (r *runtime) Subscribe(ctx context.Context, params *mcp.SubscribeParams) error {
	ctx, cancel := withTimeout(ctx, r.server.Timeout)
	defer cancel()
	return r.session.Subscribe(component.WithoutValues(ctx), params)
}

func (r *runtime) Unsubscribe(ctx context.Context, params *mcp.UnsubscribeParams) error {
	ctx, cancel := withTimeout(ctx, r.server.Timeout)
	defer cancel()
	return r.session.Unsubscribe(component.WithoutValues(ctx), params)
}

func (r *runtime) Close() error { return r.session.Close() }

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func clientWithHeaders(base *http.Client, headers map[string]string, passthroughHeaders []string) *http.Client {
	var client http.Client
	if base != nil {
		client = *base
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = headerTransport{base: transport, headers: headers, passthroughHeaders: passthroughHeaders}
	return &client
}

func downstreamCallToolParams(params *mcp.CallToolParams) *mcp.CallToolParams {
	clone := *params
	clone.Meta = component.DownstreamMeta(params.Meta)
	return &clone
}

func downstreamGetPromptParams(params *mcp.GetPromptParams) *mcp.GetPromptParams {
	clone := *params
	clone.Meta = component.DownstreamMeta(params.Meta)
	return &clone
}

func downstreamReadResourceParams(params *mcp.ReadResourceParams) *mcp.ReadResourceParams {
	clone := *params
	clone.Meta = component.DownstreamMeta(params.Meta)
	return &clone
}
func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	incoming := component.RequestHeadersFromContext(req.Context())
	for _, name := range t.passthroughHeaders {
		values := incoming.Values(name)
		if len(values) == 0 {
			continue
		}
		clone.Header.Del(name)
		for _, value := range values {
			clone.Header.Add(name, value)
		}
	}
	for name, value := range t.headers {
		clone.Header.Set(name, value)
	}
	tool, arguments := component.ToolCallFromContext(req.Context())
	addToolParamHeaders(clone.Header, tool, arguments)
	return t.base.RoundTrip(clone)
}

func addToolParamHeaders(headers http.Header, tool *mcp.Tool, arguments []byte) {
	if tool == nil || len(arguments) == 0 {
		return
	}
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return
	}
	var schema struct {
		Properties map[string]headerSchemaProperty `json:"properties"`
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(data, &schema) != nil || json.Unmarshal(arguments, &args) != nil {
		return
	}
	addAnnotatedHeaders(headers, schema.Properties, args)
}

func addAnnotatedHeaders(headers http.Header, properties map[string]headerSchemaProperty, arguments map[string]json.RawMessage) {
	for name, property := range properties {
		argument, ok := arguments[name]
		if !ok || string(argument) == "null" {
			continue
		}
		var header string
		if json.Unmarshal(property.Header, &header) == nil && header != "" {
			if value, ok := encodeParamHeader(argument); ok {
				headers.Set("Mcp-Param-"+header, value)
			}
		}
		if len(property.Properties) > 0 {
			var nested map[string]json.RawMessage
			if json.Unmarshal(argument, &nested) == nil {
				addAnnotatedHeaders(headers, property.Properties, nested)
			}
		}
	}
}

func encodeParamHeader(raw json.RawMessage) (string, bool) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	var encoded string
	switch value := value.(type) {
	case string:
		encoded = value
	case bool:
		encoded = strconv.FormatBool(value)
	case float64:
		if value != math.Trunc(value) || value < -(1<<53-1) || value > 1<<53-1 {
			return "", false
		}
		encoded = strconv.FormatInt(int64(value), 10)
	default:
		return "", false
	}
	if strings.Trim(encoded, " \t") != encoded || (strings.HasPrefix(encoded, "=?base64?") && strings.HasSuffix(encoded, "?=")) {
		return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(encoded)) + "?=", true
	}
	for _, char := range encoded {
		if char < 0x20 || char > 0x7e {
			return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(encoded)) + "?=", true
		}
	}
	return encoded, true
}
