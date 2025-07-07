package main

import (
	"context"
	"fmt"
	"io"
	argo "lmtools/argolib"
	"net/http"
	"os"
	"strings"
)

func main() {
	cfg, err := argo.ParseFlags(os.Args[1:])
	if err != nil {
		argo.Fatalf("invalid flags: %v", err)
	}
	if err := argo.InitLogging(cfg.LogLevel); err != nil {
		argo.Fatalf("invalid log-level: %v", err)
	}

	inputBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		argo.Fatalf("failed to read from STDIN: %v", err)
	}
	inputStr := strings.TrimRight(string(inputBytes), "\n")

	req, body, err := argo.BuildRequest(cfg, inputStr)
	if err != nil {
		argo.Fatalf("failed to build request: %v", err)
	}

	opName := "embed_input"
	if !cfg.Embed {
		if cfg.StreamChat {
			opName = "stream_chat_input"
		} else {
			opName = "chat_input"
		}
	}
	if err := argo.LogJSON(cfg.LogDir, opName, body); err != nil {
		argo.Fatalf("failed to log request: %v", err)
	}

	client := argo.NewHTTPClient(cfg.Timeout)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	resp, err := argo.SendRequest(ctx, client, req)
	if err != nil {
		argo.Fatalf("failed to send request: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			argo.Debugf("failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		argo.Fatalf("bad response code: %d; body: %s", resp.StatusCode, string(respBody))
	}

	out, err := argo.HandleResponse(cfg, resp)
	if err != nil {
		argo.Fatalf("failed to handle response: %v", err)
	}
	if out != "" {
		fmt.Print(out)
	}
}
