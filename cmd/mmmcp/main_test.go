package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/config"
)

type fakeApplication struct {
	started chan struct{}
	once    sync.Once
	mu      sync.Mutex
	closed  bool
}

func TestParseSettingsFlagOverridesEnvironment(t *testing.T) {
	environment := map[string]string{
		"MMMCP_CONFIG":    "environment.yaml",
		"MMMCP_TRANSPORT": "http",
		"MMMCP_LISTEN":    "127.0.0.1:9000",
		"MMMCP_DSN":       "environment.db",
	}
	parsed, err := parseSettings([]string{
		"-config=flag.yaml",
		"-transport=stdio",
		"-listen=127.0.0.1:9100",
		"-dsn=flag.db",
	}, io.Discard, func(name string) (string, bool) { value, ok := environment[name]; return value, ok })
	if err != nil {
		t.Fatal(err)
	}
	if parsed.configPath != "flag.yaml" || parsed.transport != "stdio" || parsed.listen.value != "127.0.0.1:9100" || parsed.dsn.value != "flag.db" {
		t.Fatalf("settings = %+v", parsed)
	}
}

func TestRunAppliesOverridesAndClosesAfterCancellation(t *testing.T) {
	configPath := writeConfig(t, "listen: 127.0.0.1:8000\nservers:\n  - name: fixture\n    url: http://example.invalid/mcp\n")
	started := make(chan struct{})
	fake := &fakeApplication{started: started}
	var captured *config.Config
	var capturedOptions mmmcp.Options
	deps := dependencies{
		lookupEnv: func(name string) (string, bool) {
			values := map[string]string{"MMMCP_LISTEN": "127.0.0.1:9000", "MMMCP_DSN": "environment.db"}
			value, ok := values[name]
			return value, ok
		},
		newComposite: func(_ context.Context, cfg *config.Config, opts mmmcp.Options) (application, error) {
			clone := *cfg
			captured = &clone
			capturedOptions = opts
			return fake, nil
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-config=" + configPath, "-transport=stdio", "-listen=127.0.0.1:9100", "-dsn="}, io.Discard, deps)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("stdio frontend did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after cancellation")
	}
	if captured.Listen != "127.0.0.1:9100" || capturedOptions.DSN != "" {
		t.Fatalf("effective config = %+v", captured)
	}
	if !fake.wasClosed() {
		t.Fatal("composite was not closed")
	}
}

func TestRunReportsStartupFailure(t *testing.T) {
	configPath := writeConfig(t, "servers:\n  - name: fixture\n    url: http://example.invalid/mcp\n")
	want := errors.New("startup failed")
	err := run(t.Context(), []string{"-config=" + configPath}, io.Discard, dependencies{
		lookupEnv: func(string) (string, bool) { return "", false },
		newComposite: func(context.Context, *config.Config, mmmcp.Options) (application, error) {
			return nil, want
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("run error = %v, want %v", err, want)
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mmmcp.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (f *fakeApplication) HTTPHandler() http.Handler { return http.NotFoundHandler() }

func (f *fakeApplication) RunStdio(ctx context.Context) error {
	f.once.Do(func() { close(f.started) })
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeApplication) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeApplication) wasClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}
