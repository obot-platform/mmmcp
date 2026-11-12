// Package testserver provides reusable HTTP MCP component fixtures.
package testserver

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool pairs an MCP tool definition with its raw handler.
type Tool struct {
	Definition *mcp.Tool
	Handler    mcp.ToolHandler
}

// Prompt pairs an MCP prompt definition with its handler.
type Prompt struct {
	Definition *mcp.Prompt
	Handler    mcp.PromptHandler
}

// Resource pairs an MCP resource definition with its handler.
type Resource struct {
	Definition *mcp.Resource
	Handler    mcp.ResourceHandler
}

// ResourceTemplate pairs an MCP resource-template definition with its handler.
type ResourceTemplate struct {
	Definition *mcp.ResourceTemplate
	Handler    mcp.ResourceHandler
}

// Options configures a fixture server.
type Options struct {
	Tools             []Tool
	Prompts           []Prompt
	Resources         []Resource
	ResourceTemplates []ResourceTemplate
	PageSize          int
	Headers           map[string]string
	Subscribe         func(context.Context, *mcp.SubscribeRequest) error
	Unsubscribe       func(context.Context, *mcp.UnsubscribeRequest) error
	Stateful          bool
}

// Call records one tools/call request received by the component.
type Call struct {
	Name      string
	Arguments json.RawMessage
}

// PromptGet records one prompts/get request received by the component.
type PromptGet struct {
	Name      string
	Arguments map[string]string
}

// Server is a stateless Streamable HTTP MCP component fixture.
type Server struct {
	URL string

	server       *httptest.Server
	mu           sync.Mutex
	calls        []Call
	promptGets   []PromptGet
	reads        []string
	subscribes   []string
	unsubscribes []string
	discoveries  int
	deletes      int
}

// New starts a current-protocol component fixture.
func New(tb testing.TB, opts Options) *Server {
	tb.Helper()
	fixture := &Server{}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "mmmcp-test-component", Version: "1.0.0"}, &mcp.ServerOptions{
		PageSize: opts.PageSize,
		SubscribeHandler: func(ctx context.Context, req *mcp.SubscribeRequest) error {
			fixture.mu.Lock()
			fixture.subscribes = append(fixture.subscribes, req.Params.URI)
			fixture.mu.Unlock()
			if opts.Subscribe != nil {
				return opts.Subscribe(ctx, req)
			}
			return nil
		},
		UnsubscribeHandler: func(ctx context.Context, req *mcp.UnsubscribeRequest) error {
			fixture.mu.Lock()
			fixture.unsubscribes = append(fixture.unsubscribes, req.Params.URI)
			fixture.mu.Unlock()
			if opts.Unsubscribe != nil {
				return opts.Unsubscribe(ctx, req)
			}
			return nil
		},
	})
	mcpServer.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			fixture.mu.Lock()
			switch method {
			case "server/discover":
				fixture.discoveries++
			case "tools/call":
				call := req.(*mcp.CallToolRequest)
				fixture.calls = append(fixture.calls, Call{
					Name:      call.Params.Name,
					Arguments: append(json.RawMessage(nil), call.Params.Arguments...),
				})
			case "prompts/get":
				get := req.(*mcp.GetPromptRequest)
				arguments := make(map[string]string, len(get.Params.Arguments))
				maps.Copy(arguments, get.Params.Arguments)
				fixture.promptGets = append(fixture.promptGets, PromptGet{Name: get.Params.Name, Arguments: arguments})
			case "resources/read":
				read := req.(*mcp.ReadResourceRequest)
				fixture.reads = append(fixture.reads, read.Params.URI)
			}
			fixture.mu.Unlock()
			return next(ctx, method, req)
		}
	})
	for _, tool := range opts.Tools {
		mcpServer.AddTool(tool.Definition, tool.Handler)
	}
	for _, prompt := range opts.Prompts {
		mcpServer.AddPrompt(prompt.Definition, prompt.Handler)
	}
	for _, resource := range opts.Resources {
		mcpServer.AddResource(resource.Definition, resource.Handler)
	}
	for _, template := range opts.ResourceTemplates {
		mcpServer.AddResourceTemplate(template.Definition, template.Handler)
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{
		Stateless:                    !opts.Stateful,
		JSONResponse:                 true,
		PropagateRequestCancellation: true,
	})
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, value := range opts.Headers {
			if r.Header.Get(name) != value {
				http.Error(w, "missing required fixture header", http.StatusUnauthorized)
				return
			}
		}
		if r.Method == http.MethodDelete {
			fixture.mu.Lock()
			fixture.deletes++
			fixture.mu.Unlock()
		}
		handler.ServeHTTP(w, r)
	}))
	fixture.URL = fixture.server.URL
	tb.Cleanup(fixture.Close)
	return fixture
}

// Calls returns a copy of recorded tool calls.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Call, len(s.calls))
	for i, call := range s.calls {
		result[i] = Call{Name: call.Name, Arguments: append(json.RawMessage(nil), call.Arguments...)}
	}
	return result
}

// PromptGets returns a copy of recorded prompt requests.
func (s *Server) PromptGets() []PromptGet {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]PromptGet, len(s.promptGets))
	for i, get := range s.promptGets {
		arguments := make(map[string]string, len(get.Arguments))
		maps.Copy(arguments, get.Arguments)
		result[i] = PromptGet{Name: get.Name, Arguments: arguments}
	}
	return result
}

// Reads returns a copy of recorded resource URIs.
func (s *Server) Reads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reads)
}

// Subscribes returns a copy of recorded resource subscription URIs.
func (s *Server) Subscribes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.subscribes)
}

// Unsubscribes returns a copy of recorded resource unsubscription URIs.
func (s *Server) Unsubscribes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.unsubscribes)
}

// Discoveries returns the number of downstream client sessions negotiated.
func (s *Server) Discoveries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.discoveries
}

// Deletes returns the number of downstream session-termination requests.
func (s *Server) Deletes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deletes
}

// Close stops the fixture server.
func (s *Server) Close() {
	if s != nil && s.server != nil {
		s.server.Close()
	}
}
