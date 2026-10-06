package stdio

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/config"
)

func TestPaginateTreatsMethodNotFoundAsEmptyList(t *testing.T) {
	for _, method := range []string{"tools/list", "prompts/list", "resources/list", "resources/templates/list"} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			err := paginate("fixture", method, func(string) (string, error) {
				calls++
				return "", fmt.Errorf("list response: %w", &jsonrpc.Error{
					Code:    jsonrpc.CodeMethodNotFound,
					Message: "Method not found",
				})
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("page calls = %d, want 1", calls)
			}
		})
	}
}

func TestSuccessfulOneOffReturnsBeforeStubbornProcessCleanup(t *testing.T) {
	const terminateDuration = 500 * time.Millisecond
	factory := NewFactory(Options{
		LookupEnv:         func(string) (string, bool) { return "", false },
		TerminateDuration: terminateDuration,
	})
	server := config.Server{
		Name:    "stubborn",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestStubbornStdioHelperProcess"},
		Env:     map[string]string{"MMMCP_STUBBORN_HELPER": "1"},
	}

	started := time.Now()
	result, err := factory.CallTool(t.Context(), server, &mcp.CallToolParams{Name: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= terminateDuration/2 {
		t.Fatalf("tool call waited for process cleanup: %v", elapsed)
	}
	if got := result.Content[0].(*mcp.TextContent).Text; got != "invoked" {
		t.Fatalf("tool result = %q, want invoked", got)
	}

	closeStarted := time.Now()
	if err := factory.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(closeStarted); elapsed < terminateDuration/2 {
		t.Fatalf("factory close did not wait for cleanup: %v", elapsed)
	}
	if _, err := factory.CallTool(t.Context(), server, &mcp.CallToolParams{Name: "ok"}); err == nil {
		t.Fatal("closed factory accepted a one-off operation")
	}
}

func TestStubbornStdioHelperProcess(t *testing.T) {
	if os.Getenv("MMMCP_STUBBORN_HELPER") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	server := mcp.NewServer(&mcp.Implementation{Name: "stubborn-helper", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "ok", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "invoked"}}}, nil
	})
	_ = server.Run(t.Context(), &mcp.StdioTransport{})
	for {
		time.Sleep(time.Hour)
	}
}

func TestDiscoverSkipsUnadvertisedFeatureLists(t *testing.T) {
	factory := NewFactory(Options{LookupEnv: func(string) (string, bool) { return "", false }})
	t.Cleanup(func() { _ = factory.Close() })
	server := config.Server{
		Name:    "tools-only",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestToolsOnlyStdioHelperProcess"},
		Env:     map[string]string{"MMMCP_TOOLS_ONLY_HELPER": "1"},
	}

	features, err := factory.Discover(t.Context(), server)
	if err != nil {
		t.Fatal(err)
	}
	if len(features.Tools) != 1 || features.Tools[0].Name != "echo" {
		t.Fatalf("tools = %+v, want [echo]", features.Tools)
	}
	if len(features.Prompts) != 0 {
		t.Fatalf("prompts = %+v, want none", features.Prompts)
	}
	if len(features.Resources) != 0 {
		t.Fatalf("resources = %+v, want none", features.Resources)
	}
	if len(features.ResourceTemplates) != 0 {
		t.Fatalf("resource templates = %+v, want none", features.ResourceTemplates)
	}
}

func TestToolsOnlyStdioHelperProcess(t *testing.T) {
	if os.Getenv("MMMCP_TOOLS_ONLY_HELPER") != "1" {
		return
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "tools-only-helper", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "prompts/list", "resources/list", "resources/templates/list":
				return nil, fmt.Errorf("%s is not implemented", method)
			}
			return next(ctx, method, req)
		}
	})
	_ = server.Run(t.Context(), &mcp.StdioTransport{})
}

func TestDiscoverSkipsUnadvertisedTools(t *testing.T) {
	factory := NewFactory(Options{LookupEnv: func(string) (string, bool) { return "", false }})
	t.Cleanup(func() { _ = factory.Close() })
	server := config.Server{
		Name:    "prompts-only",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestPromptsOnlyStdioHelperProcess"},
		Env:     map[string]string{"MMMCP_PROMPTS_ONLY_HELPER": "1"},
	}

	features, err := factory.Discover(t.Context(), server)
	if err != nil {
		t.Fatal(err)
	}
	if len(features.Tools) != 0 {
		t.Fatalf("tools = %+v, want none", features.Tools)
	}
	if len(features.Prompts) != 1 || features.Prompts[0].Name != "greet" {
		t.Fatalf("prompts = %+v, want [greet]", features.Prompts)
	}
}

func TestPromptsOnlyStdioHelperProcess(t *testing.T) {
	if os.Getenv("MMMCP_PROMPTS_ONLY_HELPER") != "1" {
		return
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "prompts-only-helper", Version: "1"}, nil)
	server.AddPrompt(&mcp.Prompt{Name: "greet"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{}, nil
	})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/list", "resources/list", "resources/templates/list":
				return nil, fmt.Errorf("%s is not implemented", method)
			}
			return next(ctx, method, req)
		}
	})
	_ = server.Run(t.Context(), &mcp.StdioTransport{})
}
