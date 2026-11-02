package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/config"
)

type shutdownApplication struct {
	application
	handler http.Handler
	close   func() error
}

func (a *shutdownApplication) HTTPHandler() http.Handler { return a.handler }
func (a *shutdownApplication) Close() error              { return a.close() }

func TestRunHTTPShutdown(t *testing.T) {
	for _, name := range []string{"stream", "stalled-body", "listener-error"} {
		t.Run(name, func(t *testing.T) {
			stalledBody := name != "stream"
			listenerError := name == "listener-error"
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()

			started := make(chan struct{})
			cancelStream := make(chan struct{})
			requestDone := make(chan struct{})
			var closeCalls atomic.Int32
			closeErr := errors.New("cleanup result")
			app := &shutdownApplication{
				handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(requestDone)
					close(started)
					if stalledBody {
						_, _ = io.Copy(io.Discard, r.Body)
					} else {
						w.Header().Set("Content-Type", "text/event-stream")
						w.(http.Flusher).Flush()
						<-cancelStream
					}
				}),
				close: func() error {
					closeCalls.Add(1)
					close(cancelStream)
					<-requestDone

					return closeErr
				},
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			configPath := writeConfig(t, "servers:\n  - name: fixture\n    url: http://example.invalid/mcp\n")
			done := make(chan error, 1)
			go func() {
				done <- run(ctx, []string{"-config=" + configPath}, io.Discard, dependencies{
					lookupEnv: func(string) (string, bool) { return "", false },
					listen:    func(string, string) (net.Listener, error) { return listener, nil },
					newComposite: func(context.Context, *config.Config, mmmcp.Options) (application, error) {
						return app, nil
					},
				})
			}()

			conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// Leave the advertised body incomplete to exercise forced connection closure.
			request := "GET /mcp HTTP/1.1\r\nHost: localhost\r\n\r\n"
			if stalledBody {
				request = "POST /mcp HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\n\r\n{"
			}
			if _, err := io.WriteString(conn, request); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case err := <-done:
				t.Fatalf("run exited before serving: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("request did not start")
			}

			if listenerError {
				// Failing Accept must also close connections already admitted by Serve.
				if err := listener.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}

			timeout := 5 * time.Second
			if stalledBody && !listenerError {
				timeout += shutdownTimeout
			}
			select {
			case err := <-done:
				if !errors.Is(err, closeErr) {
					t.Fatalf("run error = %v, want cleanup error", err)
				}
				if errors.Is(err, context.DeadlineExceeded) != (stalledBody && !listenerError) {
					t.Fatalf("run error = %v, stalled body = %v", err, stalledBody)
				}
				if listenerError && !errors.Is(err, net.ErrClosed) {
					t.Fatalf("run error = %v, want listener error", err)
				}
			case <-time.After(timeout):
				t.Fatal("HTTP shutdown did not finish")
			}
			if got := closeCalls.Load(); got != 1 {
				t.Fatalf("Close calls = %d, want 1", got)
			}
		})
	}
}
