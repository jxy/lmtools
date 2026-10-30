package alcfauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrLoginRequired means there is no token that can be refreshed, so the user
// has to log in again.
var ErrLoginRequired = errors.New("login required")

const (
	defaultHTTPTimeout = 30 * time.Second
	maxResponseBytes   = 1 << 20
)

// Token is one resource server's tokens as the cache stores them. ExpiresAt
// and AuthenticatedAt are Unix seconds.
type Token struct {
	Scope        string `json:"scope"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	// AuthenticatedAt is when the login that issued the refresh token ran.
	// ALCF asks for a new login every 30 days.
	AuthenticatedAt int64 `json:"authenticated_at,omitempty"`
}

// Expiry is when the access token stops working.
func (t Token) Expiry() time.Time {
	return time.Unix(t.ExpiresAt, 0)
}

// Client talks to Globus Auth for one public native client.
type Client struct {
	// AuthURL is the Globus Auth base URL; DefaultAuthURL when empty.
	AuthURL string
	// ClientID is the native app; DefaultClientID when empty.
	ClientID string
	HTTP     *http.Client
	Now      func() time.Time
}

func (c Client) authURL() string {
	if c.AuthURL == "" {
		return DefaultAuthURL
	}
	return strings.TrimRight(c.AuthURL, "/")
}

func (c Client) clientID() string {
	if c.ClientID == "" {
		return DefaultClientID
	}
	return c.ClientID
}

func (c Client) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c Client) httpClient() *http.Client {
	if c.HTTP == nil {
		return &http.Client{Timeout: defaultHTTPTimeout}
	}
	return c.HTTP
}

// AuthorizeURL is the page the user opens to log in. The parameters are the
// ones alcf-tokens sends: offline access for a refresh token, the service's
// session policy, and prompt=login so Globus authenticates the user at the
// identity provider again, which restarts ALCF's 30 day login period.
func (c Client) AuthorizeURL(svc Service, challenge, state string) string {
	q := url.Values{}
	q.Set("client_id", c.clientID())
	q.Set("redirect_uri", RedirectURI)
	q.Set("scope", svc.Scope)
	q.Set("state", state)
	q.Set("response_type", "code")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("access_type", "offline")
	q.Set("prompt", "login")
	if svc.SessionPolicy != "" {
		q.Set("session_required_policies", svc.SessionPolicy)
	}
	return c.authURL() + "/v2/oauth2/authorize?" + q.Encode()
}

// ExchangeCode trades the pasted authorization code for the service's tokens.
func (c Client) ExchangeCode(ctx context.Context, svc Service, code, verifier string) (Token, error) {
	body, err := c.postForm(ctx, "/v2/oauth2/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {c.clientID()},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {RedirectURI},
	})
	if err != nil {
		return Token{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	return parseTokenResponse(body, svc, c.now())
}

// Refresh gets a new access token. Globus may leave the refresh token out of
// the response, in which case the one passed in stays in use.
func (c Client) Refresh(ctx context.Context, svc Service, refreshToken string) (Token, error) {
	body, err := c.postForm(ctx, "/v2/oauth2/token", url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {c.clientID()},
		"refresh_token": {refreshToken},
	})
	if err != nil {
		var gerr *GlobusError
		if errors.As(err, &gerr) && gerr.Code == "invalid_grant" {
			return Token{}, fmt.Errorf("%w: Globus refused the refresh token: %v", ErrLoginRequired, err)
		}
		return Token{}, fmt.Errorf("refresh access token: %w", err)
	}
	tok, err := parseTokenResponse(body, svc, c.now())
	if err != nil {
		return Token{}, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

// Revoke invalidates an access or refresh token at Globus.
func (c Client) Revoke(ctx context.Context, token string) error {
	if _, err := c.postForm(ctx, "/v2/oauth2/token/revoke", url.Values{
		"token":     {token},
		"client_id": {c.clientID()},
	}); err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	return nil
}

func (c Client) postForm(ctx context.Context, path string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authURL()+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read Globus Auth response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, newGlobusError(resp.StatusCode, body)
	}
	return body, nil
}

// GlobusError is a failed Globus Auth request. Code is the OAuth2 error code,
// such as invalid_grant, when the body has one.
type GlobusError struct {
	StatusCode  int
	Code        string
	Description string
	Body        string
}

func newGlobusError(status int, body []byte) *GlobusError {
	e := &GlobusError{StatusCode: status, Body: strings.TrimSpace(string(body))}
	var parsed struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Errors           []struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		e.Code = parsed.Error
		e.Description = parsed.ErrorDescription
		if len(parsed.Errors) > 0 {
			if e.Code == "" {
				e.Code = parsed.Errors[0].Code
			}
			if e.Description == "" {
				e.Description = parsed.Errors[0].Detail
			}
		}
	}
	return e
}

func (e *GlobusError) Error() string {
	msg := fmt.Sprintf("Globus Auth answered HTTP %d", e.StatusCode)
	switch {
	case e.Code != "" && e.Description != "":
		return msg + ": " + e.Code + ": " + e.Description
	case e.Code != "":
		return msg + ": " + e.Code
	case e.Body != "":
		return msg + ": " + truncate(e.Body, 200)
	default:
		return msg
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// tokenResponse is Globus Auth's token endpoint answer. A login that asks for
// several resource servers gets one token at the top level and the rest in
// other_tokens.
type tokenResponse struct {
	AccessToken    string          `json:"access_token"`
	RefreshToken   string          `json:"refresh_token"`
	ExpiresIn      int64           `json:"expires_in"`
	ResourceServer string          `json:"resource_server"`
	Scope          string          `json:"scope"`
	OtherTokens    []tokenResponse `json:"other_tokens"`
}

func parseTokenResponse(body []byte, svc Service, now time.Time) (Token, error) {
	var resp tokenResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return Token{}, fmt.Errorf("parse Globus Auth token response: %w", err)
	}
	for _, t := range append([]tokenResponse{resp}, resp.OtherTokens...) {
		if t.AccessToken == "" {
			continue
		}
		if t.ResourceServer != svc.ResourceServer && !hasScope(t.Scope, svc.Scope) {
			continue
		}
		if t.ExpiresIn <= 0 {
			return Token{}, fmt.Errorf("token for %s from Globus Auth has no expires_in", svc.ResourceServer)
		}
		scope := t.Scope
		if scope == "" {
			scope = svc.Scope
		}
		return Token{
			Scope:        scope,
			AccessToken:  t.AccessToken,
			RefreshToken: t.RefreshToken,
			ExpiresAt:    now.Unix() + t.ExpiresIn,
		}, nil
	}
	return Token{}, fmt.Errorf("no token for resource server %s in the Globus Auth response", svc.ResourceServer)
}

func hasScope(scopes, scope string) bool {
	for _, s := range strings.Fields(scopes) {
		if s == scope {
			return true
		}
	}
	return false
}
