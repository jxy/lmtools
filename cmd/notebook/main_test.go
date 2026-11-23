package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"lmtools/internal/mcp"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNotebookOversizedCallReachesItsStdioClient(t *testing.T) {
	const rootEnv = "LMC_TEST_NOTEBOOK_STDIO_ROOT"
	if root := os.Getenv(rootEnv); root != "" {
		os.Args = []string{"notebook", "-root", root}
		flag.CommandLine = flag.NewFlagSet("notebook", flag.ExitOnError)
		main()
		os.Exit(0)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	client, err := mcp.Dial(ctx, mcp.ServerConfig{
		Name: "notebook", Type: mcp.TransportStdio, Command: exe,
		Args: []string{"-test.run=^TestNotebookOversizedCallReachesItsStdioClient$"},
		Env:  map[string]string{rootEnv: t.TempDir()},
	}, mcp.DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	args, err := json.Marshal(map[string]string{"query": strings.Repeat("x", 1<<20)})
	if err != nil {
		t.Fatal(err)
	}
	large := make(chan error, 1)
	go func() {
		_, err := client.CallTool(ctx, "search", args, nil)
		large <- err
	}()
	list := func() {
		t.Helper()
		result, err := client.CallTool(ctx, "list", json.RawMessage("{}"), nil)
		if err != nil || result.IsError || len(result.Content) != 1 {
			t.Fatalf("valid call on same connection: %v %v", result, err)
		}
	}
	list()
	err = <-large
	var rpc *mcp.RPCError
	if !errors.As(err, &rpc) || rpc.Code != mcp.CodeInvalidRequest || rpc.Message != "request exceeds 1048576 bytes" || ctx.Err() != nil {
		t.Fatalf("oversized caller received %v (context %v)", err, ctx.Err())
	}
	list()
}
