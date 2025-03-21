package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
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

// Request payloads for chat and embed requests.
type ChatRequest struct {
	User        string   `json:"user"`
	Model       string   `json:"model"`
	System      string   `json:"system"`
	Prompt      []string `json:"prompt"`
	Temperature float64  `json:"temperature"`
}

type EmbedRequest struct {
	User   string   `json:"user"`
	Model  string   `json:"model"`
	Prompt []string `json:"prompt"`
}

// Response structures (only the needed field is included)
type ChatResponse struct {
	Response string `json:"response"`
}

type EmbedResponse struct {
	Embedding string `json:"embedding"`
}

// logJSON writes the given JSON payload to a file under logDir with a filename based on
// the operation (e.g., "chat_input.json" or "chat_output.json"). Filenames are timestamped.
func logJSON(logDir, op string, payload []byte) {
	// ensure directory exists
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Fatalf("failed to create log directory %s: %v", logDir, err)
	}
	// Build a filename, e.g. 20231008T153045_chat_input.json
	timestamp := time.Now().Format("20060102T150405")
	filename := fmt.Sprintf("%s_%s.json", timestamp, op)
	path := filepath.Join(logDir, filename)
	if err := ioutil.WriteFile(path, payload, 0644); err != nil {
		log.Fatalf("failed to write log file %s: %v", path, err)
	}
}

func main() {
	// Define command line flags
	modelPtr := flag.String("m", "", "model to use")
	embedFlag := flag.Bool("e", false, "Set embed mode (default is chat)")
	logDirPtr := flag.String("logDir", defaultLogDir, "directory for log files")
	userPtr := flag.String("u", "xjin", "User to use")
	systemPtr := flag.String("s", "You are a brilliant assistant.", "System prompt to use in chat mode")
	flag.Parse()

	// The default URL. Uncomment the below line to use production URL if needed.
	urlBase := devURL
	// urlBase := prodURL // use production if needed

	// Read the entire STDIN; trimming any trailing newline.
	inputBytes, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		log.Fatalf("failed to read from STDIN: %v", err)
	}
	inputStr := strings.TrimRight(string(inputBytes), "\n")

	// If no model provided then use the default based on if embed mode is enabled.
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

	// Build JSON payload and request URL based on mode.
	var (
		reqBody  []byte
		endpoint string
	)

	if *embedFlag {
		// embed mode payload
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
		// chat mode payload
		chatReq := ChatRequest{
			User:        *userPtr,
			Model:       model,
			System:      *systemPtr,
			Prompt:      []string{inputStr},
			Temperature: 0.1,
		}
		reqBody, err = json.Marshal(chatReq)
		if err != nil {
			log.Fatalf("failed to marshal chat request: %v", err)
		}
		endpoint = fmt.Sprintf("%s/chat/", urlBase)
	}

	// Log the input JSON
	logJSON(*logDirPtr, func() string {
		if *embedFlag {
			return "embed_input"
		}
		return "chat_input"
	}(), reqBody)

	// Make HTTP POST request with application/json header
	resp, err := http.Post(endpoint, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		log.Fatalf("failed to POST to %s: %v", endpoint, err)
	}
	defer resp.Body.Close()

	// Check if HTTP status OK
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		log.Fatalf("bad response code: %d; body: %s", resp.StatusCode, string(bodyBytes))
	}

	// Read entire response body.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("failed to read response body: %v", err)
	}

	// Log the response JSON.
	logJSON(*logDirPtr, func() string {
		if *embedFlag {
			return "embed_output"
		}
		return "chat_output"
	}(), respBody)

	// Parse and extract the field from JSON response.
	if *embedFlag {
		var embedResp EmbedResponse
		if err := json.Unmarshal(respBody, &embedResp); err != nil {
			log.Fatalf("failed to unmarshal embed response: %v", err)
		}
		// Print the embedding to stdout.
		fmt.Print(embedResp.Embedding)
	} else {
		var chatResp ChatResponse
		if err := json.Unmarshal(respBody, &chatResp); err != nil {
			log.Fatalf("failed to unmarshal chat response: %v", err)
		}
		// Print the chat response to stdout.
		fmt.Print(chatResp.Response)
	}
}
