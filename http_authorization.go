package mmmcp

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
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
	mu   sync.Mutex
	errs []*component.AuthorizationError
}

// Streaming responses can translate setup failures only until the first commit.
// Once SSE starts, retain its normal protocol errors and never buffer the stream.
type streamingAuthorizationWriter struct {
	http.ResponseWriter
	composite *Composite
	request   *http.Request
	capture   *authorizationErrorCapture
	committed bool
	rejected  bool
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

	challenges := component.AuthorizationErrors(err)
	if len(challenges) == 0 {
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

	capture.mu.Lock()
	defer capture.mu.Unlock()

	for _, authErr := range challenges {
		clone := *authErr
		clone.Scopes = append([]string(nil), authErr.Scopes...)
		capture.errs = append(capture.errs, &clone)
	}
}

func (c *authorizationErrorCapture) Errors() []*component.AuthorizationError {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]*component.AuthorizationError(nil), c.errs...)
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
	if challenges := capture.Errors(); len(challenges) > 0 {
		c.writeAuthorizationErrors(w, r, challenges)

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

func authorizationChallenge(authErr *component.AuthorizationError) string {
	params := make([]string, 0, 6)
	if authErr.Realm != "" {
		params = append(params, "realm="+strconv.Quote(authErr.Realm))
	}

	if authErr.ErrorURI != "" {
		params = append(params, "error_uri="+strconv.Quote(authErr.ErrorURI))
	}

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

func (w *streamingAuthorizationWriter) WriteHeader(status int) {
	if w.committed {
		return
	}

	w.committed = true
	if challenges := w.capture.Errors(); len(challenges) > 0 {
		w.rejected = true
		w.composite.writeAuthorizationErrors(w.ResponseWriter, w.request, challenges)

		return
	}

	w.ResponseWriter.WriteHeader(status)
}

func (w *streamingAuthorizationWriter) Write(body []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}

	if w.rejected {
		return len(body), nil
	}

	return w.ResponseWriter.Write(body)
}

func (w *streamingAuthorizationWriter) Flush() {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}

	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (c *Composite) serveStreamingAuthorizationResponse(w http.ResponseWriter, r *http.Request, next http.Handler) {
	capture := new(authorizationErrorCapture)
	token := crand.Text()
	c.authorizationErrors.Store(token, capture)
	defer c.authorizationErrors.Delete(token)
	r.Header.Set(authorizationErrorCaptureHeader, token)
	r = r.WithContext(contextWithAuthorizationErrorCapture(r.Context(), capture))
	writer := &streamingAuthorizationWriter{
		ResponseWriter: w,
		composite:      c,
		request:        r,
		capture:        capture,
	}
	next.ServeHTTP(writer, r)
	if !writer.committed {
		writer.WriteHeader(http.StatusOK)
	}
}

func (c *Composite) writeAuthorizationErrors(w http.ResponseWriter, r *http.Request, challenges []*component.AuthorizationError) {
	if c.serverOptions.AuthorizationResponse != nil {
		c.serverOptions.AuthorizationResponse(w, r, challenges)

		return
	}

	for _, challenge := range challenges {
		w.Header().Add("WWW-Authenticate", authorizationChallenge(challenge))
	}

	http.Error(w, "component requires authorization", challenges[0].StatusCode)
}
