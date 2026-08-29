package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// LoadOptions controls environment interpolation while loading YAML.
type LoadOptions struct {
	LookupEnv func(string) (string, bool)
}

// LoadFile loads a strict YAML configuration from path.
func LoadFile(path string, opts LoadOptions) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Load(data, opts)
}

// Load loads one strict YAML document.
func Load(data []byte, opts LoadOptions) (*Config, error) {
	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var dto configDTO
	if err := decoder.Decode(&dto); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("decode config: %w", err)
		}
		return nil, errors.New("decode config: multiple YAML documents are not allowed")
	}

	cfg, err := dto.runtime(lookup)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func (d durationValue) runtime() time.Duration {
	return d.Duration
}

func (d *durationValue) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a string")
	}
	if node.Value == "" {
		d.Duration = 0
		return nil
	}
	value, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	d.Duration = value
	return nil
}

func (d configDTO) runtime(lookup func(string) (string, bool)) (*Config, error) {
	cfg := &Config{
		Name:        strings.TrimSpace(d.Name),
		Version:     strings.TrimSpace(d.Version),
		Listen:      d.Listen,
		IdleTimeout: d.IdleTimeout.runtime(),
	}
	if len(d.Servers) == 0 {
		return nil, errors.New("servers: at least one component is required")
	}
	cfg.Servers = make([]Server, len(d.Servers))
	seen := make(map[string]struct{}, len(d.Servers))
	for i := range d.Servers {
		path := fmt.Sprintf("servers[%d]", i)
		server, err := d.Servers[i].runtime(path, lookup)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[server.Name]; ok {
			return nil, fmt.Errorf("%s.name: duplicate component name %q", path, server.Name)
		}
		seen[server.Name] = struct{}{}
		cfg.Servers[i] = server
	}
	return cfg, nil
}

func (d serverDTO) runtime(path string, lookup func(string) (string, bool)) (Server, error) {
	server := Server{
		Name:               strings.TrimSpace(d.Name),
		Prefix:             d.Prefix,
		Headers:            make(map[string]string, len(d.Headers)),
		PassthroughHeaders: make([]string, len(d.PassthroughHeaders)),
		Args:               make([]string, len(d.Args)),
		Env:                make(map[string]string, len(d.Env)),
		Timeout:            d.Timeout.runtime(),
		Tools:              make([]ToolOverride, len(d.Tools)),
		Prompts:            make([]PromptOverride, len(d.Prompts)),
		Resources:          make([]ResourceOverride, len(d.Resources)),
		ResourceTemplates:  make([]ResourceTemplateOverride, len(d.ResourceTemplates)),
	}
	if server.Name == "" {
		return Server{}, fmt.Errorf("%s.name: must not be empty", path)
	}
	copy(server.PassthroughHeaders, d.PassthroughHeaders)

	fields := []struct {
		value *string
		input string
		name  string
	}{
		{&server.URL, d.URL, "url"},
		{&server.Command, d.Command, "command"},
		{&server.WorkingDirectory, d.WorkingDirectory, "workingDirectory"},
	}
	for _, field := range fields {
		value, err := interpolate(field.input, path+"."+field.name, lookup)
		if err != nil {
			return Server{}, err
		}
		*field.value = value
	}
	if (server.URL == "") == (server.Command == "") {
		return Server{}, fmt.Errorf("%s: exactly one of url or command must be set", path)
	}
	if server.URL != "" && (len(d.Args) > 0 || len(d.Env) > 0 || server.WorkingDirectory != "") {
		return Server{}, fmt.Errorf("%s: args, env, and workingDirectory require command", path)
	}
	if server.Command != "" && len(d.Headers) > 0 {
		return Server{}, fmt.Errorf("%s.headers: requires url", path)
	}
	if server.Command != "" && len(d.PassthroughHeaders) > 0 {
		return Server{}, fmt.Errorf("%s.passthroughHeaders: requires url", path)
	}

	for key, value := range d.Headers {
		interpolated, err := interpolate(value, fmt.Sprintf("%s.headers[%q]", path, key), lookup)
		if err != nil {
			return Server{}, err
		}
		server.Headers[key] = interpolated
	}
	for i, value := range d.Args {
		interpolated, err := interpolate(value, fmt.Sprintf("%s.args[%d]", path, i), lookup)
		if err != nil {
			return Server{}, err
		}
		server.Args[i] = interpolated
	}
	for key, value := range d.Env {
		interpolated, err := interpolate(value, fmt.Sprintf("%s.env[%q]", path, key), lookup)
		if err != nil {
			return Server{}, err
		}
		server.Env[key] = interpolated
	}

	for i, override := range d.Tools {
		if override.Name == "" {
			return Server{}, fmt.Errorf("%s.tools[%d].name: must not be empty", path, i)
		}
		server.Tools[i] = ToolOverride{override.Name, override.OverrideName, override.Description, override.OverrideDescription, enabledOrDefault(override.Enabled)}
	}
	for i, override := range d.Prompts {
		if override.Name == "" {
			return Server{}, fmt.Errorf("%s.prompts[%d].name: must not be empty", path, i)
		}
		server.Prompts[i] = PromptOverride{override.Name, override.OverrideName, override.Description, override.OverrideDescription, enabledOrDefault(override.Enabled)}
	}
	for i, override := range d.Resources {
		if override.URI == "" {
			return Server{}, fmt.Errorf("%s.resources[%d].uri: must not be empty", path, i)
		}
		server.Resources[i] = ResourceOverride{override.URI, override.OverrideURI, override.Name, override.OverrideName, override.Description, override.OverrideDescription, enabledOrDefault(override.Enabled)}
	}
	for i, override := range d.ResourceTemplates {
		if override.URITemplate == "" {
			return Server{}, fmt.Errorf("%s.resourceTemplates[%d].uriTemplate: must not be empty", path, i)
		}
		server.ResourceTemplates[i] = ResourceTemplateOverride{override.URITemplate, override.OverrideURITemplate, override.Name, override.OverrideName, override.Description, override.OverrideDescription, enabledOrDefault(override.Enabled)}
	}
	return server, nil
}
