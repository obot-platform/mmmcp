package catalog

import (
	"context"
	"fmt"
	"runtime"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

type memoryDiscoverer struct{}

func (memoryDiscoverer) Discover(context.Context, config.Server) (*component.Features, error) {
	tools := make([]*mcp.Tool, 50)
	for i := range tools {
		tools[i] = &mcp.Tool{
			Name:        fmt.Sprintf("lookup_record_%02d", i),
			Description: "Find a record by name, date, or account",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
					"date": map[string]any{"type": "string"},
				},
			},
		}
	}
	return &component.Features{Tools: tools}, nil
}

// BenchmarkRegistryRetainedMemory compares catalogs with and without tool search
// across shared and separate registries. Run with -benchtime=1x
// for the retained_B metric.
func BenchmarkRegistryRetainedMemory(b *testing.B) {
	for _, tc := range []struct {
		name     string
		count    int
		separate bool
	}{
		{
			name:  "shared_100",
			count: 100,
		},
		{
			name:  "shared_1000",
			count: 1000,
		},
		{
			name:     "separate_100",
			count:    100,
			separate: true,
		},
		{
			name:     "separate_1000",
			count:    1000,
			separate: true,
		},
	} {
		for _, search := range []struct {
			name    string
			enabled bool
		}{
			{
				name:    "search_on",
				enabled: true,
			},
			{
				name: "search_off",
			},
		} {
			b.Run(tc.name+"/"+search.name, func(b *testing.B) {
				for range b.N {
					runtime.GC()
					var before runtime.MemStats
					runtime.ReadMemStats(&before)

					var registry *Registry
					if !tc.separate {
						registry = NewRegistry(memoryDiscoverer{})
					}
					var separate []*Registry
					for i := range tc.count {
						if tc.separate {
							registry = NewRegistry(memoryDiscoverer{})
							separate = append(separate, registry)
						}
						cfg := &config.Config{
							Name:       fmt.Sprintf("vmcp-%03d", i),
							ToolSearch: search.enabled,
							Servers: []config.Server{{
								Name: "fixture", URL: "https://example.invalid",
							}},
						}
						compiled, _, err := registry.Get(b.Context(), cfg)
						if err != nil {
							b.Fatal(err)
						}
						if search.enabled {
							if _, err := compiled.SearchTool(b.Context(), map[string]any{"query": "record"}); err != nil {
								b.Fatal(err)
							}
						}
					}

					runtime.GC()
					var after runtime.MemStats
					runtime.ReadMemStats(&after)
					b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc), "retained_B")
					if !tc.separate {
						registry.Close()
					}
					for _, instance := range separate {
						instance.Close()
					}
					runtime.KeepAlive(registry)
					runtime.KeepAlive(separate)
				}
			})
		}
	}
}

// BenchmarkSearchIndexRetainedMemory isolates the built Bleve index from the
// search catalog and its document map. Run with -benchtime=1x.
func BenchmarkSearchIndexRetainedMemory(b *testing.B) {
	for _, built := range []bool{false, true} {
		name := "unbuilt"
		if built {
			name = "built"
		}
		b.Run(name, func(b *testing.B) {
			for range b.N {
				runtime.GC()
				var before runtime.MemStats
				runtime.ReadMemStats(&before)

				catalogs := make([]*Catalog, 0, 1000)
				for i := range 1000 {
					cfg := &config.Config{
						Name:       fmt.Sprintf("vmcp-%03d", i),
						ToolSearch: true,
						Servers:    []config.Server{{Name: "fixture", URL: "https://example.invalid"}},
					}
					compiled, err := compile(b.Context(), cfg, memoryDiscoverer{}, false)
					if err != nil {
						b.Fatal(err)
					}
					catalogs = append(catalogs, compiled)
					if built {
						compiled.StartSearchIndex(b.Context())
						if _, err := compiled.SearchTool(b.Context(), map[string]any{"query": "record"}); err != nil {
							b.Fatal(err)
						}
					}
				}

				runtime.GC()
				var after runtime.MemStats
				runtime.ReadMemStats(&after)
				b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc), "retained_B")
				for _, compiled := range catalogs {
					compiled.StopSearchIndex()
				}
				runtime.KeepAlive(catalogs)
			}
		})
	}
}
