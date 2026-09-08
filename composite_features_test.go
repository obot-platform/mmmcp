package mmmcp_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/testserver"
)

func TestCompositeRoutesEveryFeatureFamilyAndRewritesTypedURIs(t *testing.T) {
	protocolErr := &jsonrpc.Error{Code: -32042, Message: "component protocol error"}
	fixture := testserver.New(t, testserver.Options{
		PageSize: 1,
		Tools: []testserver.Tool{
			{Definition: &mcp.Tool{Name: "disabled", Description: "hidden", InputSchema: map[string]any{"type": "object"}}, Handler: emptyToolHandler},
			{Definition: &mcp.Tool{Name: "links", Description: "live tool", InputSchema: map[string]any{"type": "object"}}, Handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.ResourceLink{URI: "file:///notes", Name: "notes"},
						&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///reports/one.txt", Text: "report"}},
					},
					StructuredContent: map[string]any{"uri": "file:///notes"},
				}, nil
			}},
			{Definition: &mcp.Tool{Name: "protocol_error", InputSchema: map[string]any{"type": "object"}}, Handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, protocolErr }},
		},
		Prompts: []testserver.Prompt{
			{Definition: &mcp.Prompt{Name: "disabled", Description: "hidden"}, Handler: emptyPromptHandler},
			{Definition: &mcp.Prompt{Name: "explain", Description: "live prompt"}, Handler: func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
				return &mcp.GetPromptResult{Description: "result", Messages: []*mcp.PromptMessage{{Role: mcp.Role("user"), Content: &mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///notes", Text: req.Params.Arguments["topic"]}}}}}, nil
			}},
		},
		Resources: []testserver.Resource{
			{Definition: &mcp.Resource{URI: "file:///disabled", Name: "disabled"}, Handler: emptyResourceHandler},
			{Definition: &mcp.Resource{URI: "file:///notes", Name: "notes", Description: "live resource"}, Handler: func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "notes"}}}, nil
			}},
		},
		ResourceTemplates: []testserver.ResourceTemplate{
			{Definition: &mcp.ResourceTemplate{URITemplate: "file:///reports/{name}", Name: "reports", Description: "live template"}, Handler: func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "report"}}}, nil
			}},
		},
	})
	cfg := &config.Config{Servers: []config.Server{{
		Name: "Docs Server", URL: fixture.URL,
		Tools: []config.ToolOverride{
			{Name: "protocol_error", Enabled: true},
			{Name: "disabled", Enabled: false},
			{Name: "links", OverrideName: "references", OverrideDescription: "overridden tool", Enabled: true},
		},
		Prompts: []config.PromptOverride{
			{Name: "disabled", Enabled: false},
			{Name: "explain", OverrideName: "describe", OverrideDescription: "overridden prompt", Enabled: true},
		},
		Resources: []config.ResourceOverride{
			{URI: "file:///disabled", Enabled: false},
			{URI: "file:///notes", OverrideURI: "file:///public-notes", OverrideName: "public notes", OverrideDescription: "overridden resource", Enabled: true},
		},
		ResourceTemplates: []config.ResourceTemplateOverride{{URITemplate: "file:///reports/{name}", OverrideURITemplate: "file:///public-reports/{name}", OverrideName: "public reports", OverrideDescription: "overridden template", Enabled: true}},
	}}}
	composite, err := mmmcp.New(t.Context(), cfg, mmmcp.Options{PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer composite.Close()
	frontend := httptest.NewServer(composite.HTTPHandler())
	defer frontend.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "feature-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: frontend.URL, HTTPClient: frontend.Client(), DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools := collectTools(t, session)
	if len(tools) != 2 || tools[0].Name != "protocol_error" || tools[1].Name != "references" || tools[1].Description != "overridden tool" {
		t.Fatalf("tools = %+v", tools)
	}
	prompts := collectPrompts(t, session)
	if len(prompts) != 1 || prompts[0].Name != "describe" || prompts[0].Description != "overridden prompt" {
		t.Fatalf("prompts = %+v", prompts)
	}
	resources := collectResources(t, session)
	if len(resources) != 1 || resources[0].URI != "file:///public-notes" || resources[0].Name != "public notes" || resources[0].Description != "overridden resource" {
		t.Fatalf("resources = %+v", resources)
	}
	templates := collectTemplates(t, session)
	if len(templates) != 1 || templates[0].URITemplate != "file:///public-reports/{name}" || templates[0].Name != "public reports" || templates[0].Description != "overridden template" {
		t.Fatalf("templates = %+v", templates)
	}

	toolResult, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "references", Arguments: map[string]any{"value": "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResult.Content[0].(*mcp.ResourceLink).URI; got != "file:///public-notes" {
		t.Fatalf("tool resource link URI = %q", got)
	}
	if got := toolResult.Content[1].(*mcp.EmbeddedResource).Resource.URI; got != "file:///public-reports/one.txt" {
		t.Fatalf("tool embedded URI = %q", got)
	}
	if got := toolResult.StructuredContent.(map[string]any)["uri"]; got != "file:///notes" {
		t.Fatalf("structured content URI = %v", got)
	}
	if calls := fixture.Calls(); len(calls) != 1 || calls[0].Name != "links" {
		t.Fatalf("tool calls = %+v", calls)
	}

	promptResult, err := session.GetPrompt(t.Context(), &mcp.GetPromptParams{Name: "describe", Arguments: map[string]string{"topic": "routing"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := promptResult.Messages[0].Content.(*mcp.EmbeddedResource).Resource.URI; got != "file:///public-notes" {
		t.Fatalf("prompt embedded URI = %q", got)
	}
	if gets := fixture.PromptGets(); len(gets) != 1 || gets[0].Name != "explain" || gets[0].Arguments["topic"] != "routing" {
		t.Fatalf("prompt gets = %+v", gets)
	}

	readResult, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "file:///public-notes"})
	if err != nil {
		t.Fatal(err)
	}
	if readResult.Contents[0].URI != "file:///public-notes" {
		t.Fatalf("read URI = %q", readResult.Contents[0].URI)
	}
	templateResult, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "file:///public-reports/two.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if templateResult.Contents[0].URI != "file:///public-reports/two.txt" {
		t.Fatalf("template read URI = %q", templateResult.Contents[0].URI)
	}
	if reads := fixture.Reads(); len(reads) != 2 || reads[0] != "file:///notes" || reads[1] != "file:///reports/two.txt" {
		t.Fatalf("component reads = %v", reads)
	}

	_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "protocol_error"})
	var rpcErr *jsonrpc.Error
	if err == nil || !errors.As(err, &rpcErr) && !strings.Contains(err.Error(), protocolErr.Message) {
		t.Fatalf("protocol error = %v", err)
	}
}

func collectTools(t *testing.T, session *mcp.ClientSession) []*mcp.Tool {
	t.Helper()
	var result []*mcp.Tool
	var cursor string
	for {
		page, err := session.ListTools(t.Context(), &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, page.Tools...)
		if page.NextCursor == "" {
			return result
		}
		cursor = page.NextCursor
	}
}

func collectPrompts(t *testing.T, session *mcp.ClientSession) []*mcp.Prompt {
	t.Helper()
	var result []*mcp.Prompt
	var cursor string
	for {
		page, err := session.ListPrompts(t.Context(), &mcp.ListPromptsParams{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, page.Prompts...)
		if page.NextCursor == "" {
			return result
		}
		cursor = page.NextCursor
	}
}

func collectResources(t *testing.T, session *mcp.ClientSession) []*mcp.Resource {
	t.Helper()
	var result []*mcp.Resource
	var cursor string
	for {
		page, err := session.ListResources(t.Context(), &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, page.Resources...)
		if page.NextCursor == "" {
			return result
		}
		cursor = page.NextCursor
	}
}

func collectTemplates(t *testing.T, session *mcp.ClientSession) []*mcp.ResourceTemplate {
	t.Helper()
	var result []*mcp.ResourceTemplate
	var cursor string
	for {
		page, err := session.ListResourceTemplates(t.Context(), &mcp.ListResourceTemplatesParams{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, page.ResourceTemplates...)
		if page.NextCursor == "" {
			return result
		}
		cursor = page.NextCursor
	}
}

func emptyToolHandler(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{}, nil
}

func emptyPromptHandler(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{}}, nil
}

func emptyResourceHandler(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{}}, nil
}
