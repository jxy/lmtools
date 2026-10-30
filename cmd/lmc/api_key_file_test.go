package main

import (
	"context"
	"lmtools/internal/mockserver"
	"os"
	"path/filepath"
	"testing"
)

// alcfauth refreshes a Globus access token by renaming a new key file over
// the old one while lmc runs. That works only because lmc reads -api-key-file
// for every request; loading the key once at startup would leave a long REPL
// sending the token that expired.
func TestEachRequestReadsTheKeyFileAgain(t *testing.T) {
	server := mockserver.NewMockServer(mockserver.WithDefaultResponse("ok"))
	defer server.Close()
	keyFile := filepath.Join(t.TempDir(), "inference.key")
	replaceKeyFile := func(token string) {
		t.Helper()
		tmp := keyFile + ".tmp"
		if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, keyFile); err != nil {
			t.Fatal(err)
		}
	}
	replaceKeyFile("first-token")
	te := newTurnTestEnv(t, server, "-api-key-file", keyFile)

	if _, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "one"}); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	replaceKeyFile("second-token")
	if _, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "two"}); err != nil {
		t.Fatalf("second turn: %v", err)
	}

	requests := server.GetRequests()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want 2", len(requests))
	}
	for i, want := range []string{"Bearer first-token", "Bearer second-token"} {
		if got := requests[i].Headers.Get("Authorization"); got != want {
			t.Errorf("request %d Authorization = %q, want %q", i+1, got, want)
		}
	}
}
