package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// default values as defined in the original script
const (
	prodURL           = "https://apps.inside.anl.gov/argoapi/api/v1/resource"
	devURL            = "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource"
	defaultChatModel  = "gpto3mini"
	defaultEmbedModel = "text-embedding-3-large"
)

// use a variable so we can compute the log directory from the environment.
var defaultLogDir = os.ExpandEnv("$HOME/tmp/log/argo")

// EmbedRequest for non streaming embed requests.
type EmbedRequest struct {
	User   string   `json:"user"`
	Model  string   `json:"model"`
	Prompt []string `json:"prompt"`
}

// StreamChatRequest for streaming chat requests.
type StreamChatRequest struct {
	User     string    `json:"user"`
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// logJSON writes the given JSON payload to a file under logDir with a filename based on op.
func logJSON(logDir, op string, payload []byte) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Fatalf("failed to create log directory %s: %v", logDir, err)
	}
	timestamp := time.Now().Format("20060102T150405")
	filename := fmt.Sprintf("%s_%s.json", timestamp, op)
	path := filepath.Join(logDir, filename)
	if err := os.WriteFile(path, payload, 0644); err != nil {
		log.Fatalf("failed to write log file %s: %v", path, err)
	}
}

func main() {
	modelPtr := flag.String("m", "", "model to use")
	embedFlag := flag.Bool("e", false, "Set embed mode (default is chat streaming)")
	logDirPtr := flag.String("logDir", defaultLogDir, "directory for log files")
	userPtr := flag.String("u", "xjin", "User to use")
	systemPtr := flag.String("s", "You are a brilliant assistant.", "System prompt to use in chat mode")
	flag.Parse()

	urlBase := devURL

	inputBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Fatalf("failed to read from STDIN: %v", err)
	}
	inputStr := strings.TrimRight(string(inputBytes), "\n")

	model := *modelPtr
	if *embedFlag {
		if model == "" {
			model = defaultEmbedModel
		}
	} else {
		if model == "" {
			model = defaultChatModel
		}
	}

	var (
		reqBody  []byte
		endpoint string
	)

	if *embedFlag {
		embedReq := EmbedRequest{
			User:   *userPtr,
			Model:  model,
			Prompt: []string{inputStr},
		}
		reqBody, err = json.Marshal(embedReq)
		if err != nil {
			log.Fatalf("failed to marshal embed request: %v", err)
		}
		endpoint = fmt.Sprintf("%s/embed/", urlBase)
	} else {
		streamChatReq := StreamChatRequest{
			User:  *userPtr,
			Model: model,
			Messages: []Message{
				{Role: "system", Content: *systemPtr},
				{Role: "user", Content: inputStr},
			},
		}
		reqBody, err = json.Marshal(streamChatReq)
		if err != nil {
			log.Fatalf("failed to marshal stream chat request: %v", err)
		}
		endpoint = fmt.Sprintf("%s/streamchat/", urlBase)
	}

	opName := "embed_input"
	if !*embedFlag {
		opName = "stream_chat_input"
	}
	logJSON(*logDirPtr, opName, reqBody)

	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(reqBody))
	if err != nil {
		log.Fatalf("failed to create HTTP request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("failed to POST to %s: %v", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		log.Fatalf("bad response code: %d; body: %s", resp.StatusCode, string(bodyBytes))
	}

	if *embedFlag {
		// For embed mode, read the full response and log it.
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Fatalf("failed to read response body: %v", err)
		}
		logJSON(*logDirPtr, "embed_output", respBody)
		var embedResp struct {
			Embedding string `json:"embedding"`
		}
		if err := json.Unmarshal(respBody, &embedResp); err != nil {
			log.Fatalf("failed to unmarshal embed response: %v", err)
		}
		fmt.Print(embedResp.Embedding)
	} else {
		// For streaming chat, log the full response to a file while copying it unbuffered to stdout.
		if err := os.MkdirAll(*logDirPtr, 0755); err != nil {
			log.Fatalf("failed to create log directory %s: %v", *logDirPtr, err)
		}
		timestamp := time.Now().Format("20060102T150405")
		logFilename := fmt.Sprintf("%s_stream_chat_output.log", timestamp)
		logPath := filepath.Join(*logDirPtr, logFilename)
		logFile, err := os.Create(logPath)
		if err != nil {
			log.Fatalf("failed to create log file %s: %v", logPath, err)
		}
		defer logFile.Close()

		tee := io.TeeReader(resp.Body, logFile)
		if _, err := io.Copy(os.Stdout, tee); err != nil {
			log.Fatalf("error streaming response: %v", err)
		}
	}
}
