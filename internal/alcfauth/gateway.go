package alcfauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	lmerrors "lmtools/internal/errors"
	"net/http"
	"strings"
)

// Gateway is the ALCF inference gateway's own API, beside the OpenAI
// compatible routes lmc uses.
type Gateway struct {
	// URL is the gateway root; DefaultGatewayURL when empty.
	URL  string
	HTTP *http.Client
}

// User is the gateway's whoami answer.
type User struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	IdPName  string `json:"idp_name"`
}

// Whoami asks the gateway who the access token belongs to. A 200 answer means
// the gateway accepts the token.
func (g Gateway) Whoami(ctx context.Context, accessToken string) (User, error) {
	body, err := g.get(ctx, accessToken, "whoami")
	if err != nil {
		return User{}, err
	}
	var u User
	if err := json.Unmarshal(body, &u); err != nil {
		return User{}, fmt.Errorf("parse whoami response: %w", err)
	}
	return u, nil
}

func (g Gateway) get(ctx context.Context, accessToken, path string) ([]byte, error) {
	base := g.URL
	if base == "" {
		base = DefaultGatewayURL
	}
	target := strings.TrimRight(base, "/") + "/" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read gateway response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("GET %s: %w", target, lmerrors.NewHTTPError(resp.StatusCode, string(body)))
	}
	return body, nil
}
