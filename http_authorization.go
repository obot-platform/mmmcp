package mmmcp

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/component"
)

const (
	authorizationErrorCaptureHeader = "X-Mmmcp-Authorization-Capture"
	subscriptionsListenMethod       = "subscriptions/listen"
)

type authorizationErrorCaptureKey struct{}

type authorizationErrorCapture struct {
	mu  sync.Mutex
	err *component.AuthorizationError
}

type bufferedResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func contextWithAuthorizationErrorCapture(ctx context.Context, capture *authorizationErrorCapture) context.Context {
	return context.WithValue(ctx, authorizationErrorCaptureKey{}, capture)
}

func (c *Composite) captureAuthorizationError(ctx context.Context, request mcp.Request, err error) {
	if err == nil {
		return
	}
	authErr, ok := errors.AsType[*component.AuthorizationError](err)
	if !ok || authErr == nil || authErr.StatusCode != http.StatusUnauthorized && authErr.StatusCode != http.StatusForbidden {
		return
	}
	var capture *authorizationErrorCapture
	if request != nil {
		if extra := request.GetExtra(); extra != nil && extra.Header != nil {
			value, _ := c.authorizationErrors.Load(extra.Header.Get(authorizationErrorCaptureHeader))
			capture, _ = value.(*authorizationErrorCapture)
		}
	}
	if capture == nil {
		capture, _ = ctx.Value(authorizationErrorCaptureKey{}).(*authorizationErrorCapture)
	}
	if capture == nil {
		return
	}
	clone := *authErr
	clone.Scopes = append([]string(nil), authErr.Scopes...)
	capture.mu.Lock()
	if capture.err == nil {
		capture.err = &clone
	}
	capture.mu.Unlock()
}

func (c *authorizationErrorCapture) Error() *component.AuthorizationError {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Composite) serveBufferedAuthorizationResponse(w http.ResponseWriter, r *http.Request, next http.Handler) {
	capture := new(authorizationErrorCapture)
	token := crand.Text()
	c.authorizationErrors.Store(token, capture)
	defer c.authorizationErrors.Delete(token)
	r.Header.Set(authorizationErrorCaptureHeader, token)
	r = r.WithContext(contextWithAuthorizationErrorCapture(r.Context(), capture))
	buffer := newBufferedResponseWriter()
	next.ServeHTTP(buffer, r)
	if authErr := capture.Error(); authErr != nil {
		writeAuthorizationError(w, authErr)
		return
	}
	buffer.WriteTo(w)
}

func isSubscriptionsListenRequest(r *http.Request) bool {
	if r.Body == nil {
		return false
	}
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return false
	}
	var request struct {
		Method string `json:"method"`
	}
	return json.Unmarshal(body, &request) == nil && request.Method == subscriptionsListenMethod
}

func writeAuthorizationError(w http.ResponseWriter, authErr *component.AuthorizationError) {
	w.Header().Set("WWW-Authenticate", authorizationChallenge(authErr))
	http.Error(w, authErr.Error(), authErr.StatusCode)
}

func authorizationChallenge(authErr *component.AuthorizationError) string {
	params := make([]string, 0, 4)
	if authErr.ResourceMetadata != "" {
		params = append(params, "resource_metadata="+strconv.Quote(authErr.ResourceMetadata))
	}
	scope := authErr.Scope
	if scope == "" {
		scope = strings.Join(authErr.Scopes, " ")
	}
	if scope != "" {
		params = append(params, "scope="+strconv.Quote(scope))
	}
	if authErr.ErrorCode != "" {
		params = append(params, "error="+strconv.Quote(authErr.ErrorCode))
	}
	if authErr.ErrorDescription != "" {
		params = append(params, "error_description="+strconv.Quote(authErr.ErrorDescription))
	}
	if len(params) == 0 {
		return "Bearer"
	}
	return "Bearer " + strings.Join(params, ", ")
}

func newBufferedResponseWriter() *bufferedResponseWriter {
	return &bufferedResponseWriter{header: make(http.Header)}
}

func (w *bufferedResponseWriter) Header() http.Header {
	return w.header
}

func (w *bufferedResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *bufferedResponseWriter) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(value)
}

func (*bufferedResponseWriter) Flush() {}

func (w *bufferedResponseWriter) WriteTo(destination http.ResponseWriter) {
	maps.Copy(destination.Header(), w.header)
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	destination.WriteHeader(status)
	_, _ = destination.Write(w.body.Bytes())
}
