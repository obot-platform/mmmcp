package catalog

import (
	"context"
	"fmt"

	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

// Compile discovers every configured component and starts search indexing in the background.
// The supplied context controls the background builder after Compile returns.
func Compile(ctx context.Context, cfg *config.Config, discoverer component.Discoverer) (*Catalog, error) {
	if cfg == nil {
		return nil, fmt.Errorf("catalog: nil config")
	}
	if discoverer == nil {
		return nil, fmt.Errorf("catalog: nil discoverer")
	}
	compiled, err := compile(ctx, cfg, discoverer, cfg.DeferAuthorization)
	if err != nil {
		return nil, err
	}
	compiled.StartSearchIndex(ctx)
	return compiled, nil
}
