package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"lmtools/internal/logger"
	"net/http"
	"net/http/httputil"
)

// logWireBytes deliberately emits the complete payload. DEBUG is the operator's
// opt-in full wire trace, so neither callers nor this helper may truncate it.
func logWireBytes(ctx context.Context, label string, data []byte) {
	log := logger.From(ctx)
	if !log.IsDebugEnabled() {
		return
	}
	log.Debugf("%s:\n%s", label, string(data))
}

func logWireHTTPRequest(ctx context.Context, label string, req *http.Request, body []byte) {
	log := logger.From(ctx)
	if !log.IsDebugEnabled() || req == nil {
		return
	}

	clone := req.Clone(ctx)
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	clone.ContentLength = int64(len(body))

	var (
		dump []byte
		err  error
	)
	if clone.RequestURI != "" {
		dump, err = httputil.DumpRequest(clone, true)
	} else {
		dump, err = httputil.DumpRequestOut(clone, true)
	}
	if err != nil {
		log.Debugf("%s dump failed: %v", label, err)
		return
	}
	logWireBytes(ctx, label, dump)
}

func logWireClientRequest(ctx context.Context, req *http.Request, body []byte) {
	if _, ok := ctx.Value(wireRequestBodyLoggerKey{}).(*wireRequestBodyLogger); ok {
		// Middleware already logged the parsed request head and every body read at
		// the client boundary. Re-dumping here would duplicate and reorder it.
		return
	}
	logWireHTTPRequest(ctx, "WIRE CLIENT REQUEST", req, body)
}

// logWireHTTPRequestHeaders records the parsed request head before the handler
// can produce a response. The body is deliberately separate: request bodies
// can straddle a locally generated response when the rejection drain observes
// bytes after that response has already been flushed.
func logWireHTTPRequestHeaders(ctx context.Context, label string, req *http.Request) {
	log := logger.From(ctx)
	if !log.IsDebugEnabled() || req == nil {
		return
	}

	clone := req.Clone(ctx)
	var (
		dump []byte
		err  error
	)
	if clone.RequestURI != "" {
		dump, err = httputil.DumpRequest(clone, false)
	} else {
		dump, err = httputil.DumpRequestOut(clone, false)
	}
	if err != nil {
		log.Debugf("%s dump failed: %v", label, err)
		return
	}
	logWireBytes(ctx, label, dump)
}

// wireRequestBodyLogger sits below MaxBytesReader in DEBUG mode. That placement
// matters: MaxBytesReader consumes one sentinel byte beyond its limit to prove
// the request is oversized but does not return that byte to its caller. Logging
// in Read, rather than buffering until handler cleanup, keeps each chunk on the
// same side of the response boundary where the proxy observed it.
type wireRequestBodyLogger struct {
	ctx context.Context
}

type wireRequestBodyLoggerKey struct{}

func newWireRequestBodyLogger(ctx context.Context, req *http.Request) *wireRequestBodyLogger {
	if !logger.From(ctx).IsDebugEnabled() {
		return nil
	}
	logWireHTTPRequestHeaders(ctx, "WIRE CLIENT REQUEST", req)
	return &wireRequestBodyLogger{ctx: ctx}
}

func (l *wireRequestBodyLogger) Write(p []byte) (int, error) {
	if len(p) > 0 {
		logWireBytes(l.ctx, "WIRE CLIENT REQUEST BODY", p)
	}
	return len(p), nil
}

type wireLoggingRequestReadCloser struct {
	io.ReadCloser
	wireBody *wireRequestBodyLogger
}

func (r *wireLoggingRequestReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		_, _ = r.wireBody.Write(p[:n])
	}
	return n, err
}

func logWireHTTPResponseHeaders(ctx context.Context, label string, resp *http.Response) {
	log := logger.From(ctx)
	if !log.IsDebugEnabled() || resp == nil {
		return
	}

	var buf bytes.Buffer
	proto := resp.Proto
	if proto == "" {
		proto = "HTTP/1.1"
	}
	status := resp.Status
	if status == "" {
		status = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	fmt.Fprintf(&buf, "%s %s\r\n", proto, status)
	_ = resp.Header.Write(&buf)
	buf.WriteString("\r\n")
	logWireBytes(ctx, label, buf.Bytes())
}

func logWireHTTPClientResponseHeaders(ctx context.Context, label string, statusCode int, header http.Header) {
	log := logger.From(ctx)
	if !log.IsDebugEnabled() {
		return
	}

	var buf bytes.Buffer
	status := fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode))
	fmt.Fprintf(&buf, "HTTP/1.1 %s\r\n", status)
	_ = header.Write(&buf)
	buf.WriteString("\r\n")
	logWireBytes(ctx, label, buf.Bytes())
}

func wrapWireLoggedResponseBody(ctx context.Context, label string, resp *http.Response) {
	if !logger.From(ctx).IsDebugEnabled() || resp == nil || resp.Body == nil {
		return
	}
	resp.Body = &wireLoggingReadCloser{
		ctx:   ctx,
		label: label,
		rc:    resp.Body,
	}
}

type wireLoggingReadCloser struct {
	ctx   context.Context
	label string
	rc    io.ReadCloser
}

func (r *wireLoggingReadCloser) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	if n > 0 {
		// Log every read in full. Cumulative accounting here would turn a long
		// response or stream into a partial wire trace.
		logWireBytes(r.ctx, r.label, p[:n])
	}
	return n, err
}

func (r *wireLoggingReadCloser) Close() error {
	return r.rc.Close()
}
