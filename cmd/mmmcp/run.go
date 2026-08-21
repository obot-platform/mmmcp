package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/config"
)

const (
	defaultConfigPath = "mmmcp.yaml"
	defaultListen     = "127.0.0.1:8080"
	shutdownTimeout   = 10 * time.Second
)

type application interface {
	HTTPHandler() http.Handler
	RunStdio(context.Context) error
	Close() error
}

type dependencies struct {
	lookupEnv    func(string) (string, bool)
	listen       func(string, string) (net.Listener, error)
	newComposite func(context.Context, *config.Config, mmmcp.Options) (application, error)
}

type settings struct {
	configPath string
	transport  string
	listen     optionalString
	dsn        optionalString
}

type optionalString struct {
	value string
	set   bool
}

func defaultDependencies() dependencies {
	return dependencies{
		lookupEnv: os.LookupEnv,
		listen:    net.Listen,
		newComposite: func(ctx context.Context, cfg *config.Config, opts mmmcp.Options) (application, error) {
			return mmmcp.New(ctx, cfg, opts)
		},
	}
}

func (v *optionalString) String() string { return v.value }

func (v *optionalString) Set(value string) error {
	v.value = value
	v.set = true
	return nil
}

func run(ctx context.Context, args []string, stderr io.Writer, deps dependencies) error {
	if deps.lookupEnv == nil {
		deps.lookupEnv = os.LookupEnv
	}
	if deps.listen == nil {
		deps.listen = net.Listen
	}
	if deps.newComposite == nil {
		deps.newComposite = defaultDependencies().newComposite
	}
	parsed, err := parseSettings(args, stderr, deps.lookupEnv)
	if err != nil {
		return err
	}
	loaded, err := config.LoadFile(parsed.configPath, config.LoadOptions{LookupEnv: deps.lookupEnv})
	if err != nil {
		return err
	}
	effective := *loaded
	if parsed.listen.set {
		effective.Listen = parsed.listen.value
	}
	if effective.Listen == "" {
		effective.Listen = defaultListen
	}

	logger := slog.New(slog.NewTextHandler(stderr, nil))
	options := mmmcp.Options{Logger: logger, LookupEnv: deps.lookupEnv}
	if parsed.dsn.set {
		options.DSN = parsed.dsn.value
	}
	composite, err := deps.newComposite(ctx, &effective, options)
	if err != nil {
		return err
	}
	var serveErr error
	switch parsed.transport {
	case "http":
		serveErr = serveHTTP(ctx, effective.Listen, composite.HTTPHandler(), deps.listen, logger)
	case "stdio":
		serveErr = composite.RunStdio(ctx)
	}
	if ctx.Err() != nil && (serveErr == nil || errors.Is(serveErr, context.Canceled)) {
		serveErr = nil
	}
	return errors.Join(serveErr, composite.Close())
}

func parseSettings(args []string, stderr io.Writer, lookupEnv func(string) (string, bool)) (settings, error) {
	parsed := settings{configPath: defaultConfigPath, transport: "http"}
	if value, ok := lookupEnv("MMMCP_CONFIG"); ok && value != "" {
		parsed.configPath = value
	}
	if value, ok := lookupEnv("MMMCP_TRANSPORT"); ok && value != "" {
		parsed.transport = value
	}
	if value, ok := lookupEnv("MMMCP_LISTEN"); ok {
		parsed.listen = optionalString{value: value, set: true}
	}
	if value, ok := lookupEnv("MMMCP_DSN"); ok {
		parsed.dsn = optionalString{value: value, set: true}
	}

	flags := flag.NewFlagSet("mmmcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&parsed.configPath, "config", parsed.configPath, "path to the YAML configuration")
	flags.StringVar(&parsed.transport, "transport", parsed.transport, "frontend transport: http or stdio")
	flags.Var(&parsed.listen, "listen", "HTTP listen address (overrides YAML)")
	flags.Var(&parsed.dsn, "dsn", "storage DSN (empty selects SQLite)")
	if err := flags.Parse(args); err != nil {
		return settings{}, err
	}
	if flags.NArg() != 0 {
		return settings{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	parsed.configPath = strings.TrimSpace(parsed.configPath)
	parsed.transport = strings.ToLower(strings.TrimSpace(parsed.transport))
	if parsed.configPath == "" {
		return settings{}, errors.New("config path must not be empty")
	}
	if parsed.transport != "http" && parsed.transport != "stdio" {
		return settings{}, fmt.Errorf("unsupported transport %q", parsed.transport)
	}
	return parsed, nil
}

func serveHTTP(ctx context.Context, address string, handler http.Handler, listen func(string, string) (net.Listener, error), logger *slog.Logger) error {
	listener, err := listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	logger.Info("serving MCP over HTTP", "listen", listener.Addr().String())
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()

	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("shut down HTTP server: %w", err)
		}
		err := <-done
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
