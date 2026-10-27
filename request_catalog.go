package mmmcp

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/namespace"
	"github.com/obot-platform/mmmcp/toolsearch"
)

// requestCatalogConfig is shared by policy resolution and execution. A public
// operation must not discover unrelated protected feature families or components.
func (c *Composite) requestCatalogConfig(cfg *config.Config, method, name string, arguments json.RawMessage) *config.Config {
	if !cfg.RequestScopedDiscovery {
		return cfg
	}

	clone := *cfg
	clone.Servers = slices.Clone(cfg.Servers)
	family, _, _ := strings.Cut(method, "/")

	switch family {
	case "tools", "prompts", "resources":
	default:
		family = "initialize"
	}

	for i := range clone.Servers {
		clone.Servers[i].DiscoveryMethod = family
	}

	if method != "tools/call" || cfg.DeferAuthorization {
		return &clone
	}

	if cfg.ToolSearch {
		if name != toolsearch.CallToolName {
			return &clone
		}

		args, err := toolsearch.ParseCallArguments(arguments)
		if err != nil {
			clone.Servers = nil

			return &clone
		}

		name = args.Name
	}

	// Prefixes are only candidates: local tool names can themselves contain another
	// component's prefix. Retain every match until the catalog resolves exact ownership.
	var candidates []config.Server

	for _, server := range clone.Servers {
		prefix := server.Prefix
		if len(cfg.Servers) > 1 || prefix != "" {
			var err error
			prefix, err = namespace.Prefix(server.Name, prefix)
			if err != nil {
				continue
			}

			marker, err := namespace.Tool(prefix, "x")
			if err != nil || !strings.HasPrefix(name, strings.TrimSuffix(marker, "x")) {
				continue
			}
		}

		server.Prefix = prefix
		candidates = append(candidates, server)
	}

	selected := clone
	if len(candidates) > 0 {
		selected.Servers = candidates
	}

	// A listed catalog can identify the owner without rediscovering other matching
	// prefixes, whose credentials may no longer permit discovery for this request.
	if c.registry != nil {
		for _, cachedConfig := range []*config.Config{&clone, &selected} {
			if compiled, ok := c.registry.Cached(cachedConfig); ok {
				if route, ok := compiled.RouteTool(name); ok {
					server := route.Component
					server.Prefix = route.Prefix
					selected.Servers = []config.Server{server}
					break
				}
			}
		}
	}

	return &selected
}
