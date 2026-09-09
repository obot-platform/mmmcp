package mmmcp

import (
	"context"

	"github.com/obot-platform/mmmcp/config"
)

type configContextKey struct{}
type configIDContextKey struct{}
type dsnContextKey struct{}

// ContextWithConfigID identifies a configuration across stateless requests and
// notification subscriptions. The ID must remain stable across configuration
// changes, and all requests sharing an ID must have the same configuration view.
// An empty ID disables shared notification tracking.
func ContextWithConfigID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, configIDContextKey{}, id)
}

// ConfigIDFromContext returns the nonempty configuration ID attached to ctx.
func ConfigIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(configIDContextKey{}).(string)
	return id, ok && id != ""
}

// ContextWithConfig attaches a complete configuration snapshot to ctx.
func ContextWithConfig(ctx context.Context, cfg *config.Config) context.Context {
	if cfg == nil {
		panic("mmmcp: nil config")
	}
	return context.WithValue(ctx, configContextKey{}, cfg)
}

// ConfigFromContext returns the complete configuration snapshot attached to ctx.
func ConfigFromContext(ctx context.Context) (*config.Config, bool) {
	cfg, ok := ctx.Value(configContextKey{}).(*config.Config)
	return cfg, ok && cfg != nil
}

// ContextWithDSN selects event storage for work performed with ctx. An empty
// DSN explicitly selects SQLite.
func ContextWithDSN(ctx context.Context, dsn string) context.Context {
	return context.WithValue(ctx, dsnContextKey{}, dsn)
}

// DSNFromContext returns the event storage DSN attached to ctx.
func DSNFromContext(ctx context.Context) (string, bool) {
	dsn, ok := ctx.Value(dsnContextKey{}).(string)
	return dsn, ok
}

func effectiveConfig(ctx context.Context, defaultConfig *config.Config) *config.Config {
	if cfg, ok := ConfigFromContext(ctx); ok {
		return cfg
	}
	return defaultConfig
}

func effectiveDSN(ctx context.Context, defaultDSN string) string {
	if dsn, ok := DSNFromContext(ctx); ok {
		return dsn
	}
	return defaultDSN
}
