package component

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWithoutValuesPreservesLifecycle(t *testing.T) {
	type key struct{}
	parent, cancel := context.WithTimeout(context.WithValue(t.Context(), key{}, "private"), time.Hour)
	ctx := WithoutValues(parent)
	if ctx.Value(key{}) != nil {
		t.Fatal("value was not suppressed")
	}
	if deadline, ok := ctx.Deadline(); !ok || deadline.IsZero() {
		t.Fatal("deadline was not preserved")
	}
	cancel()
	<-ctx.Done()
	if ctx.Err() != context.Canceled {
		t.Fatalf("Err() = %v, want context canceled", ctx.Err())
	}
}

func TestWithoutValuesPreservesForwardedValues(t *testing.T) {
	type key struct{}
	headers := http.Header{"X-Tenant": {"tenant-a"}}
	info := &mcp.Implementation{Name: "frontend", Version: "1"}
	parent := ContextWithClientInfo(ContextWithRequestHeaders(context.WithValue(t.Context(), key{}, "private"), headers), info)
	headers.Set("X-Tenant", "mutated")
	info.Name = "mutated"

	ctx := WithoutValues(parent)
	if ctx.Value(key{}) != nil {
		t.Fatal("private value was not suppressed")
	}
	got := RequestHeadersFromContext(ctx)
	if got.Get("X-Tenant") != "tenant-a" {
		t.Fatalf("request header = %q, want tenant-a", got.Get("X-Tenant"))
	}
	got.Set("X-Tenant", "also-mutated")
	if again := RequestHeadersFromContext(ctx).Get("X-Tenant"); again != "tenant-a" {
		t.Fatalf("stored request header was mutated to %q", again)
	}
	if got := ClientInfoFromContext(ctx); got == nil || got.Name != "frontend" || got.Version != "1" {
		t.Fatalf("client info = %+v, want frontend/1", got)
	}
}

func TestClientInfoSnapshot(t *testing.T) {
	want := mcp.Implementation{
		Name: "frontend", Version: "1", Title: "Frontend", Description: "Frontend client",
		WebsiteURL: "https://example.com",
		Icons:      []mcp.Icon{{Source: "https://example.com/icon.png", MIMEType: "image/png", Sizes: []string{"48x48"}, Theme: "light"}},
	}
	info := want
	info.Icons = []mcp.Icon{want.Icons[0]}
	info.Icons[0].Sizes = []string{"48x48"}
	ctx := WithoutValues(ContextWithClientInfo(t.Context(), &info))
	info.Title = "mutated"
	info.Icons[0].Source = "mutated"
	info.Icons[0].Sizes[0] = "mutated"
	got := ClientInfoFromContext(ctx)
	if !reflect.DeepEqual(got, &want) {
		t.Fatalf("client info = %+v, want %+v", got, want)
	}
	got.Title = "also-mutated"
	got.Icons[0].Source = "also-mutated"
	got.Icons[0].Sizes[0] = "also-mutated"
	if again := ClientInfoFromContext(ctx); !reflect.DeepEqual(again, &want) {
		t.Fatalf("stored client info was mutated: %+v", again)
	}
}

func TestDownstreamMetaRemovesFrontendProtocolMetadata(t *testing.T) {
	meta := mcp.Meta{
		mcp.MetaKeyProtocolVersion:    "2026-07-28",
		mcp.MetaKeyClientInfo:         map[string]any{"name": "frontend"},
		mcp.MetaKeyClientCapabilities: map[string]any{"tools": map[string]any{}},
		"application":                 "preserved",
	}
	clean := DownstreamMeta(meta)
	if len(clean) != 1 || clean["application"] != "preserved" {
		t.Fatalf("DownstreamMeta() = %#v, want only application metadata", clean)
	}
	if len(meta) != 4 {
		t.Fatal("DownstreamMeta mutated its input")
	}
}
