package catalog

import (
	"context"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/namespace"
	"github.com/yosida95/uritemplate/v3"
)

const (
	// maxParallelDiscoveries bounds simultaneous network connections and child processes.
	maxParallelDiscoveries = 8
)

type discoveryResult struct {
	features *component.Features
}

func compile(ctx context.Context, cfg *config.Config, discoverer component.Discoverer) (*Catalog, error) {
	result := newCompileCatalog()
	prefixes := make([]string, len(cfg.Servers))
	for i, server := range cfg.Servers {
		if len(cfg.Servers) <= 1 && server.Prefix == "" {
			continue
		}
		prefix, err := namespace.Prefix(server.Name, server.Prefix)
		if err != nil {
			return nil, fmt.Errorf("component %q: %w", server.Name, err)
		}
		prefixes[i] = prefix
	}

	discovered, err := discoverComponents(ctx, cfg.Servers, prefixes, discoverer)
	if err != nil {
		return nil, err
	}
	if len(discovered) == 1 && discovered[0].features.ServerInfo != nil {
		result.serverInfo = &mcp.Implementation{
			Name:    discovered[0].features.ServerInfo.Name,
			Version: discovered[0].features.ServerInfo.Version,
		}
	}
	templateOwners := make(map[string]string)
	for i, server := range cfg.Servers {
		if err := compileComponent(result, templateOwners, server, prefixes[i], discovered[i].features); err != nil {
			return nil, err
		}
	}
	return newCatalog(result)
}

func newCompileCatalog() *Catalog {
	return &Catalog{
		toolRoutes: make(map[string]ToolRoute), promptRoutes: make(map[string]PromptRoute),
		resourceRoutes: make(map[string]ResourceRoute),
	}
}

func compileComponent(result *Catalog, templateOwners map[string]string, server config.Server, prefix string, features *component.Features) error {
	if err := compileTools(result, server, prefix, features.Tools); err != nil {
		return err
	}
	if err := compilePrompts(result, server, prefix, features.Prompts); err != nil {
		return err
	}
	if err := compileResources(result, server, prefix, features.Resources); err != nil {
		return err
	}
	return compileTemplates(result, templateOwners, server, prefix, features.ResourceTemplates)
}

func validateComponent(server config.Server, prefix string, features *component.Features) error {
	return compileComponent(newCompileCatalog(), make(map[string]string), server, prefix, features)
}

func discoverComponents(ctx context.Context, servers []config.Server, prefixes []string, discoverer component.Discoverer) ([]discoveryResult, error) {
	results := make([]discoveryResult, len(servers))
	if len(servers) == 0 {
		return results, nil
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	workerCount := min(len(servers), maxParallelDiscoveries)
	var (
		nextMu  sync.Mutex
		workers sync.WaitGroup
		next    int
	)
	for range workerCount {
		workers.Go(func() {
			for {
				nextMu.Lock()
				if ctx.Err() != nil || next >= len(servers) {
					nextMu.Unlock()
					return
				}
				i := next
				next++
				nextMu.Unlock()

				features, err := discoverer.Discover(ctx, servers[i])
				if err == nil && features == nil {
					err = fmt.Errorf("component %q returned nil features", servers[i].Name)
				}
				if err == nil {
					err = validateComponent(servers[i], prefixes[i], features)
				}
				if err != nil {
					cancel(err)
					return
				}
				results[i].features = features
			}
		})
	}
	workers.Wait()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return results, nil
}

func compileTools(c *Catalog, server config.Server, prefix string, discovered []*mcp.Tool) error {
	if server.DisableTools {
		return nil
	}
	overrides, err := keyedOverrides(server.Name, "tool", server.Tools, func(v config.ToolOverride) string { return v.Name })
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, tool := range discovered {
		if tool == nil {
			return fmt.Errorf("component %q returned a nil tool", server.Name)
		}
		if seen[tool.Name] {
			return fmt.Errorf("component %q returned duplicate tool %q", server.Name, tool.Name)
		}
		seen[tool.Name] = true
		override, ok := overrides[tool.Name]
		if len(overrides) > 0 && (!ok || !override.Enabled) {
			continue
		}
		local := tool.Name
		if ok && override.OverrideName != "" {
			local = override.OverrideName
		}
		name, err := namespace.Tool(prefix, local)
		if err != nil {
			return fmt.Errorf("component %q: %w", server.Name, err)
		}
		if existing, ok := c.toolRoutes[name]; ok {
			return collision("tool name", name, existing.Component.Name, server.Name)
		}
		clone := *tool
		clone.Name = name
		if ok && override.OverrideDescription != "" {
			clone.Description = override.OverrideDescription
		}
		c.tools = append(c.tools, &clone)
		c.toolRoutes[name] = ToolRoute{Component: server, Prefix: prefix, OriginalName: tool.Name}
	}
	return nil
}

func compilePrompts(c *Catalog, server config.Server, prefix string, discovered []*mcp.Prompt) error {
	overrides, err := keyedOverrides(server.Name, "prompt", server.Prompts, func(v config.PromptOverride) string { return v.Name })
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, prompt := range discovered {
		if prompt == nil {
			return fmt.Errorf("component %q returned a nil prompt", server.Name)
		}
		if seen[prompt.Name] {
			return fmt.Errorf("component %q returned duplicate prompt %q", server.Name, prompt.Name)
		}
		seen[prompt.Name] = true
		override, ok := overrides[prompt.Name]
		if ok && !override.Enabled {
			continue
		}
		local := prompt.Name
		if ok && override.OverrideName != "" {
			local = override.OverrideName
		}
		name, err := namespace.Prompt(prefix, local)
		if err != nil {
			return fmt.Errorf("component %q: %w", server.Name, err)
		}
		if existing, ok := c.promptRoutes[name]; ok {
			return collision("prompt name", name, existing.Component.Name, server.Name)
		}
		clone := *prompt
		clone.Name = name
		if ok && override.OverrideDescription != "" {
			clone.Description = override.OverrideDescription
		}
		c.prompts = append(c.prompts, &clone)
		c.promptRoutes[name] = PromptRoute{Component: server, Prefix: prefix, OriginalName: prompt.Name}
	}
	return nil
}

func compileResources(c *Catalog, server config.Server, prefix string, discovered []*mcp.Resource) error {
	overrides, err := keyedOverrides(server.Name, "resource", server.Resources, func(v config.ResourceOverride) string { return v.URI })
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, resource := range discovered {
		if resource == nil {
			return fmt.Errorf("component %q returned a nil resource", server.Name)
		}
		if seen[resource.URI] {
			return fmt.Errorf("component %q returned duplicate resource %q", server.Name, resource.URI)
		}
		seen[resource.URI] = true
		override, ok := overrides[resource.URI]
		if ok && !override.Enabled {
			continue
		}
		localURI := resource.URI
		if ok && override.OverrideURI != "" {
			localURI = override.OverrideURI
		}
		uri, err := namespace.Resource(prefix, localURI)
		if err != nil {
			return fmt.Errorf("component %q: %w", server.Name, err)
		}
		if existing, ok := c.resourceRoutes[uri]; ok {
			return collision("resource URI", uri, existing.Component.Name, server.Name)
		}
		clone := *resource
		clone.URI = uri
		if ok && override.OverrideName != "" {
			clone.Name = override.OverrideName
		}
		if ok && override.OverrideDescription != "" {
			clone.Description = override.OverrideDescription
		}
		c.resources = append(c.resources, &clone)
		c.resourceRoutes[uri] = ResourceRoute{Component: server, Prefix: prefix, OriginalURI: resource.URI, CompositeURI: uri}
	}
	return nil
}

func compileTemplates(c *Catalog, owners map[string]string, server config.Server, prefix string, discovered []*mcp.ResourceTemplate) error {
	overrides, err := keyedOverrides(server.Name, "resource template", server.ResourceTemplates, func(v config.ResourceTemplateOverride) string { return v.URITemplate })
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, template := range discovered {
		if template == nil {
			return fmt.Errorf("component %q returned a nil resource template", server.Name)
		}
		if seen[template.URITemplate] {
			return fmt.Errorf("component %q returned duplicate resource template %q", server.Name, template.URITemplate)
		}
		seen[template.URITemplate] = true
		override, ok := overrides[template.URITemplate]
		if ok && !override.Enabled {
			continue
		}
		localURI := template.URITemplate
		if ok && override.OverrideURITemplate != "" {
			localURI = override.OverrideURITemplate
		}
		uri, err := namespace.ResourceTemplate(prefix, localURI)
		if err != nil {
			return fmt.Errorf("component %q: %w", server.Name, err)
		}
		if existing, ok := owners[uri]; ok {
			return collision("resource template URI", uri, existing, server.Name)
		}
		clone := *template
		clone.URITemplate = uri
		if ok && override.OverrideName != "" {
			clone.Name = override.OverrideName
		}
		if ok && override.OverrideDescription != "" {
			clone.Description = override.OverrideDescription
		}
		exposed, _ := uritemplate.New(uri)
		original, _ := uritemplate.New(template.URITemplate)
		c.resourceTemplates = append(c.resourceTemplates, &clone)
		owners[uri] = server.Name
		c.templateRoutes = append(c.templateRoutes, ResourceTemplateRoute{Component: server, Prefix: prefix, OriginalTemplate: template.URITemplate, CompositeTemplate: uri, exposed: exposed, original: original})
	}
	return nil
}

func keyedOverrides[T any](componentName, family string, values []T, key func(T) string) (map[string]T, error) {
	result := make(map[string]T, len(values))
	for _, value := range values {
		identity := key(value)
		if _, ok := result[identity]; ok {
			return nil, fmt.Errorf("component %q has duplicate %s override %q", componentName, family, identity)
		}
		result[identity] = value
	}
	return result, nil
}

func collision(family, identity, first, second string) error {
	return fmt.Errorf("composite %s %q collides between components %q and %q", family, identity, first, second)
}
