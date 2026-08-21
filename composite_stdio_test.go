package mmmcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/config"
)

var (
	stdioHelperOnce sync.Once
	stdioHelperPath string
	stdioHelperErr  error
)

type stdioInfo struct {
	PID int               `json:"pid"`
	Env map[string]string `json:"env"`
}

func TestCompositeStdioStatelessUsesFreshProcessesAndSanitizedEnvironment(t *testing.T) {
	lookup := map[string]string{
		"PATH":                       os.Getenv("PATH"),
		"PWD":                        os.Getenv("PWD"),
		"MMMCP_HOST_SENTINEL_SECRET": "must-not-leak",
	}
	server := stdioServerConfig(map[string]string{"EXPLICIT_VALUE": "configured"})
	composite, err := mmmcp.New(t.Context(), &config.Config{Servers: []config.Server{server}}, mmmcp.Options{
		LookupEnv:                func(name string) (string, bool) { value, ok := lookup[name]; return value, ok },
		CommandTerminateDuration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer composite.Close()
	frontend := httptest.NewServer(composite.HTTPHandler())
	defer frontend.Close()
	session := connectCurrent(t, frontend)
	defer session.Close()

	first := callStdioInfo(t, session)
	second := callStdioInfo(t, session)
	if first.PID == second.PID {
		t.Fatalf("stateless calls reused PID %d", first.PID)
	}
	if first.Env["EXPLICIT_VALUE"] != "configured" {
		t.Fatalf("explicit environment = %q", first.Env["EXPLICIT_VALUE"])
	}
	if _, ok := first.Env["MMMCP_HOST_SENTINEL_SECRET"]; ok {
		t.Fatalf("host secret leaked to child: %#v", first.Env)
	}
}

func TestCompositeStdioStatefulIsolationReuseIdleRestartAndCleanup(t *testing.T) {
	server := stdioServerConfig(nil)
	composite, err := mmmcp.New(t.Context(), &config.Config{
		IdleTimeout: 80 * time.Millisecond,
		Servers:     []config.Server{server},
	}, mmmcp.Options{CommandTerminateDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	frontend := httptest.NewServer(composite.HTTPHandler())
	defer frontend.Close()
	first := connectLegacy(t, frontend, nil)
	second := connectLegacy(t, frontend, nil)

	firstPID := callStdioInfo(t, first).PID
	if got := callStdioInfo(t, first).PID; got != firstPID {
		t.Fatalf("stateful session did not reuse PID: first=%d second=%d", firstPID, got)
	}
	secondPID := callStdioInfo(t, second).PID
	if secondPID == firstPID {
		t.Fatalf("frontend sessions shared PID %d", firstPID)
	}

	time.Sleep(150 * time.Millisecond)
	restartedPID := callStdioInfo(t, first).PID
	if restartedPID == firstPID {
		t.Fatalf("idle process did not restart: PID %d", firstPID)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := composite.Close(); err != nil {
		t.Fatal(err)
	}
	waitProcessGone(t, restartedPID)
	waitProcessGone(t, secondPID)
}

func TestCompositeStdioActiveCallDefersIdleRetirementAndCancellation(t *testing.T) {
	composite, err := mmmcp.New(t.Context(), &config.Config{
		IdleTimeout: 40 * time.Millisecond,
		Servers:     []config.Server{stdioServerConfig(nil)},
	}, mmmcp.Options{CommandTerminateDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer composite.Close()
	frontend := httptest.NewServer(composite.HTTPHandler())
	defer frontend.Close()
	session := connectLegacy(t, frontend, nil)
	defer session.Close()
	pid := callStdioInfo(t, session).PID

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "block"})
		done <- err
	}()
	time.Sleep(120 * time.Millisecond)
	process, err := os.FindProcess(pid)
	if err != nil || process.Signal(syscall.Signal(0)) != nil {
		t.Fatalf("active call process %d exited during idle window", pid)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled stdio call unexpectedly succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled stdio call did not return")
	}
	// The SDK delivers notifications/cancelled asynchronously. Let that write
	// finish before closing the frontend session to avoid racing its connection.
	time.Sleep(100 * time.Millisecond)
}

func stdioServerConfig(explicit map[string]string) config.Server {
	env := make(map[string]string)
	maps.Copy(env, explicit)
	return config.Server{Name: "fixture", Command: stdioHelperBinary(), Env: env}
}

func stdioHelperBinary() string {
	stdioHelperOnce.Do(func() {
		directory, err := os.MkdirTemp("", "mmmcp-stdio-helper-")
		if err != nil {
			stdioHelperErr = err
			return
		}
		stdioHelperPath = filepath.Join(directory, "stdio-helper")
		command := exec.Command("go", "build", "-o", stdioHelperPath, "./testserver/stdiohelper")
		command.Dir = "."
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		if err := command.Run(); err != nil {
			stdioHelperErr = fmt.Errorf("build stdio helper: %w: %s", err, output.String())
		}
	})
	if stdioHelperErr != nil {
		panic(stdioHelperErr)
	}
	return stdioHelperPath
}

func connectCurrent(t *testing.T, frontend *httptest.Server) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-current-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: frontend.URL, HTTPClient: frontend.Client(), DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func callStdioInfo(t *testing.T, session *mcp.ClientSession) stdioInfo {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "info"})
	if err != nil {
		t.Fatal(err)
	}
	var info stdioInfo
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &info); err != nil {
		t.Fatal(err)
	}
	return info
}

func waitProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		process, err := os.FindProcess(pid)
		if err != nil || process.Signal(syscall.Signal(0)) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d still running", pid)
}
