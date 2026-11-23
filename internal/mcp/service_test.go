package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type stubService struct{}

func (stubService) Definitions() []Tool { return []Tool{ObjectTool("test", "Test", map[string]any{})} }

func (stubService) CallTool(context.Context, string, json.RawMessage) (any, error) { return "ok", nil }

func TestServeStdioBothErasAndMalformedFrame(t *testing.T) {
	in := strings.Join([]string{
		`{`, `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"test","arguments":{}}}`,
	}, "\n")
	var out bytes.Buffer
	if err := ServeStdio(context.Background(), strings.NewReader(in), &out, Implementation{Name: "test", Version: "1"}, "", stubService{}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatal(out.String())
	}
	for i, part := range []string{"-32700", "supportedVersions", "inputSchema", "protocolVersion", `"content"`} {
		if !strings.Contains(lines[i], part) {
			t.Fatal(lines[i])
		}
	}
	if strings.Contains(lines[4], "resultType") {
		t.Fatal(lines[4])
	}
}

func TestTextPaging(t *testing.T) {
	body := "a明b"
	part, next, err := SliceText(body, 0, 3, 8)
	if err != nil || part != "a" || next != 1 {
		t.Fatal(part, next, err)
	}
	part, next, err = SliceText(body, next, 0, 8)
	if err != nil || part != "明b" || next != 0 {
		t.Fatal(part, next, err)
	}
	for _, offset := range []int{-1, 2, 20} {
		if _, _, err := SliceText(body, offset, 8, 8); err == nil {
			t.Fatal(offset)
		}
	}
}

func TestTextPagingAcrossInvalidBytes(t *testing.T) {
	body := "😀" + strings.Repeat("\x80", 9000) + "界"
	offset := 0
	var result strings.Builder
	for {
		part, next, err := SliceText(body, offset, 1000, 1000)
		if err != nil || part == "" || !utf8.ValidString(part) {
			t.Fatal(offset, next, err)
		}
		result.WriteString(part)
		if next == 0 {
			break
		}
		if next <= offset || next-offset > 1000 {
			t.Fatal(offset, next)
		}
		offset = next
	}
	if got := result.String(); got != "😀"+strings.Repeat("�", 9000)+"界" {
		t.Fatal("lost bytes from a page")
	}
}

type waitingService struct {
	stubService
	started, cancelled chan struct{}
}

func (s waitingService) CallTool(ctx context.Context, _ string, args json.RawMessage) (any, error) {
	if string(args) == `{"wait":true}` {
		close(s.started)
		<-ctx.Done()
		close(s.cancelled)
		return nil, ctx.Err()
	}
	return "immediate", nil
}

func TestServeStdioConcurrentCallsAndCancellation(t *testing.T) {
	service := waitingService{started: make(chan struct{}), cancelled: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	in, send := io.Pipe()
	out, reply := io.Pipe()
	defer send.Close()
	defer out.Close()
	done := make(chan error, 1)
	go func() { err := ServeStdio(ctx, in, reply, Implementation{}, "", service); reply.Close(); done <- err }()
	decoder := json.NewDecoder(out)
	write := func(s string) {
		t.Helper()
		if _, err := io.WriteString(send, s+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(id string) {
		t.Helper()
		var msg message
		if err := decoder.Decode(&msg); err != nil || string(msg.ID) != id || msg.Error != nil {
			t.Fatalf("wanted %s, got %+v, %v", id, msg, err)
		}
	}
	write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	expect("1")
	write(`{"jsonrpc":"2.0","id":"long","method":"tools/call","params":{"name":"test","arguments":{"wait":true}}}`)
	select {
	case <-service.started:
	case <-ctx.Done():
		t.Fatal("slow call never started")
	}
	write(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"test"}}`)
	expect("2")
	write(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"l\u006fng"}}`)
	select {
	case <-service.cancelled:
	case <-ctx.Done():
		t.Fatal("server ignored the cancellation")
	}
	write(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	expect("3")
	send.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeStdioContinuesAfterOversizedFrames(t *testing.T) {
	ping := `{"jsonrpc":"2.0","id":17,"method":"ping"}`
	atLimit := ping + strings.Repeat(" ", maxServiceFrameBytes-len(ping))
	for _, ending := range []string{"\n", "\r\n"} {
		input := strings.Join([]string{atLimit, atLimit + " ", strings.Repeat("x", 3*maxServiceFrameBytes), ping}, ending)
		var out bytes.Buffer
		if err := ServeStdio(context.Background(), strings.NewReader(input), &out, Implementation{}, "", stubService{}); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) != 4 {
			t.Fatalf("got %d replies", len(lines))
		}
		for i, line := range lines {
			var got message
			if err := json.Unmarshal([]byte(line), &got); err != nil {
				t.Fatal(err)
			}
			if i == 0 || i == 3 {
				if string(got.ID) != "17" || got.Error != nil {
					t.Fatalf("lost the ping at %d: %s", i, line)
				}
			} else {
				wantID := "17"
				if i == 2 {
					wantID = "null"
				}
				if string(got.ID) != wantID || got.Error == nil || got.Error.Code != CodeInvalidRequest {
					t.Fatalf("oversized frame at %d: %s", i, line)
				}
			}
		}
	}
}

func TestOversizedFrameIdentifiesOnlyItsTopLevelRequest(t *testing.T) {
	args, err := json.Marshal(map[string]any{"id": "nested", "text": strings.Repeat(`[}{,"id":"nested\quoted"]`, 1<<16)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ member, id string }{
		{`,"id":9007199254740993`, "9007199254740993"},
		{`,"\u0069d":"late\"identifier"`, `"late\"identifier"`},
		{`,"id":false`, "null"},
		{`,"id":7,"id":8`, "null"},
		{"", "null"},
	} {
		input := `{"jsonrpc":"2.0","params":` + string(args) + tc.member + `,"method":"tools/call"}`
		var out bytes.Buffer
		if err := ServeStdio(context.Background(), strings.NewReader(input), &out, Implementation{}, "", stubService{}); err != nil {
			t.Fatal(err)
		}
		var reply message
		if err := json.Unmarshal(out.Bytes(), &reply); err != nil || reply.Error == nil || string(reply.ID) != tc.id {
			t.Fatalf("ID field %q gave %q: %v", tc.member, out.String(), err)
		}
	}
}
