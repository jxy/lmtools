package apifixtures

import (
	"testing"
	"time"
)

func TestExtractGoogleRetryDelay(t *testing.T) {
	body := []byte(`{
  "error": {
    "details": [
      {
        "@type": "type.googleapis.com/google.rpc.Help"
      },
      {
        "@type": "type.googleapis.com/google.rpc.RetryInfo",
        "retryDelay": "22.5s"
      }
    ]
  }
}`)

	if got := extractGoogleRetryDelay(body); got != 22500*time.Millisecond {
		t.Fatalf("extractGoogleRetryDelay() = %v, want 22.5s", got)
	}
}
