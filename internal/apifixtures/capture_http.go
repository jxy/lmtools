package apifixtures

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/retry"
	"math"
	"math/rand"
	"net"
	"net/http"
	"time"
)

const googleRetryInfoType = "type.googleapis.com/google.rpc.RetryInfo"

func DoCaptureRequest(ctx context.Context, client *http.Client, req *http.Request, body []byte, provider string, cfg *retry.Config) (*http.Response, []byte, error) {
	if cfg == nil {
		cfg = retry.ProviderConfig(provider)
	}
	if cfg == nil {
		cfg = retry.DefaultConfig()
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	var lastResp *http.Response
	var lastBody []byte
	var overrideBackoff time.Duration

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := calculateCaptureBackoff(cfg, attempt-1, rng)
			if overrideBackoff > 0 {
				backoff = overrideBackoff
				overrideBackoff = 0
			}
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		reqClone := req.Clone(ctx)
		reqClone.Body = io.NopCloser(bytes.NewReader(body))
		reqClone.ContentLength = int64(len(body))

		resp, err := client.Do(reqClone)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() && attempt < cfg.MaxRetries {
				continue
			}
			return nil, nil, err
		}

		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, nil, err
		}

		lastResp = cloneHTTPResponse(resp, data)
		lastBody = append(lastBody[:0], data...)

		if !shouldRetryCaptureStatus(resp.StatusCode) {
			return cloneHTTPResponse(resp, data), data, nil
		}

		if retryAfter := retry.ExtractRetryAfter(lastResp); retryAfter > 0 {
			nextBackoff := calculateCaptureBackoff(cfg, attempt, rng)
			if retryAfter > nextBackoff {
				overrideBackoff = retryAfter
			}
		} else if retryAfter := extractProviderRetryDelay(provider, data); retryAfter > 0 {
			nextBackoff := calculateCaptureBackoff(cfg, attempt, rng)
			if retryAfter > nextBackoff {
				overrideBackoff = retryAfter
			}
		}
	}

	if lastResp != nil {
		return lastResp, lastBody, nil
	}
	return nil, nil, fmt.Errorf("capture request failed without response")
}

func shouldRetryCaptureStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		425,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		if statusCode >= 400 && statusCode < 500 {
			return false
		}
		return statusCode >= 500
	}
}

func calculateCaptureBackoff(cfg *retry.Config, attempt int, rng *rand.Rand) time.Duration {
	backoff := float64(cfg.InitialBackoff) * math.Pow(cfg.BackoffFactor, float64(attempt))
	jitter := (rng.Float64() - 0.5) * 0.5
	backoff = backoff * (1 + jitter)
	if cfg.MaxBackoff > 0 && backoff > float64(cfg.MaxBackoff) {
		backoff = float64(cfg.MaxBackoff)
	}
	return time.Duration(backoff)
}

func cloneHTTPResponse(resp *http.Response, body []byte) *http.Response {
	if resp == nil {
		return nil
	}
	clone := new(http.Response)
	*clone = *resp
	clone.Header = resp.Header.Clone()
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	return clone
}

func extractProviderRetryDelay(provider string, body []byte) time.Duration {
	switch provider {
	case "google":
		return extractGoogleRetryDelay(body)
	default:
		return 0
	}
}

func extractGoogleRetryDelay(body []byte) time.Duration {
	var payload struct {
		Error struct {
			Details []struct {
				Type       string `json:"@type"`
				RetryDelay string `json:"retryDelay"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0
	}
	for _, detail := range payload.Error.Details {
		if detail.Type != googleRetryInfoType {
			continue
		}
		delay, err := time.ParseDuration(detail.RetryDelay)
		if err == nil && delay > 0 {
			return delay
		}
	}
	return 0
}
