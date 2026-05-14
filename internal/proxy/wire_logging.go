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
		logWireBytes(r.ctx, r.label, p[:n])
	}
	return n, err
}

func (r *wireLoggingReadCloser) Close() error {
	return r.rc.Close()
}
