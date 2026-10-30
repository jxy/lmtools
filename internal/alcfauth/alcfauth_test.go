package alcfauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/auth"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var testNow = time.Unix(1_793_377_319, 0)

// globusCall is one request the fake Globus Auth received.
type globusCall struct {
	Path string
	Form url.Values
}

// fakeGlobus answers Globus Auth's token and revoke endpoints with respond
// and records every request.
type fakeGlobus struct {
	*httptest.Server
	mu    sync.Mutex
	calls []globusCall
}

func newFakeGlobus(t *testing.T, respond func(call globusCall) (int, any)) *fakeGlobus {
	t.Helper()
	f := &fakeGlobus{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		call := globusCall{Path: r.URL.Path, Form: r.PostForm}
		f.mu.Lock()
		f.calls = append(f.calls, call)
		f.mu.Unlock()
		status, body := respond(call)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeGlobus) recorded() []globusCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]globusCall(nil), f.calls...)
}

func inferenceTokenJSON(access, refresh string) map[string]any {
	tok := map[string]any{
		"access_token":    access,
		"expires_in":      172800,
		"resource_server": Inference.ResourceServer,
		"scope":           Inference.Scope,
		"token_type":      "Bearer",
	}
	if refresh != "" {
		tok["refresh_token"] = refresh
	}
	return tok
}

// loginResponse is the shape Globus returns when the login also produced a
// token for Globus Auth itself: the inference token is in other_tokens.
func loginResponse(access, refresh string) map[string]any {
	return map[string]any{
		"access_token":    "auth-access",
		"expires_in":      172800,
		"resource_server": "auth.globus.org",
		"scope":           "openid",
		"token_type":      "Bearer",
		"other_tokens":    []any{inferenceTokenJSON(access, refresh)},
	}
}

func testStore(t *testing.T, authURL string) Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "alcf")
	return Store{
		CachePath:   filepath.Join(dir, "tokens.json"),
		KeyFilePath: filepath.Join(dir, "inference.key"),
		Service:     Inference,
		Client: Client{
			AuthURL: authURL,
			Now:     func() time.Time { return testNow },
		},
	}
}

// writeCache puts a cache file in place the way an earlier login would have.
func writeCache(t *testing.T, path string, cache map[string]any) {
	t.Helper()
	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return out
}

func cachedEntry(t *testing.T, path, resourceServer string) map[string]any {
	t.Helper()
	servers, _ := readJSON(t, path)["by_resource_server"].(map[string]any)
	entry, _ := servers[resourceServer].(map[string]any)
	if entry == nil {
		t.Fatalf("no %s entry in %s", resourceServer, path)
	}
	return entry
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestChallengeS256MatchesRFC7636(t *testing.T) {
	// RFC 7636 appendix B.
	got := challengeS256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	if want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"; got != want {
		t.Fatalf("challenge = %q, want %q", got, want)
	}
}

func TestNewPKCEDrawsAVerifierOfTheMinimumLength(t *testing.T) {
	p, err := newPKCE(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Verifier) != 43 {
		t.Errorf("verifier length = %d, want 43", len(p.Verifier))
	}
	if strings.ContainsAny(p.Verifier, "+/=") {
		t.Errorf("verifier %q is not base64url without padding", p.Verifier)
	}
	if p.Challenge != challengeS256(p.Verifier) {
		t.Error("challenge does not match the verifier")
	}
	q, err := newPKCE(nil)
	if err != nil {
		t.Fatal(err)
	}
	if q.Verifier == p.Verifier {
		t.Error("two verifiers are equal")
	}
}

func TestAuthorizeURLCarriesTheLoginParameters(t *testing.T) {
	raw := Client{}.AuthorizeURL(Inference, "the-challenge", "the-state")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != "https://auth.globus.org/v2/oauth2/authorize" {
		t.Errorf("endpoint = %s", got)
	}
	want := map[string]string{
		"client_id":                 DefaultClientID,
		"redirect_uri":              "https://auth.globus.org/v2/web/auth-code",
		"scope":                     "https://auth.globus.org/scopes/681c10cc-f684-4540-bcd7-0b4df3bc26ef/action_all",
		"state":                     "the-state",
		"response_type":             "code",
		"code_challenge":            "the-challenge",
		"code_challenge_method":     "S256",
		"access_type":               "offline",
		"prompt":                    "login",
		"session_required_policies": "83732ff2-9c42-4548-b5ce-17e498c84f6a",
	}
	q := u.Query()
	for key, value := range want {
		if got := q.Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
	if len(q) != len(want) {
		t.Errorf("query has %d parameters, want %d: %v", len(q), len(want), q)
	}

	custom := Client{AuthURL: "https://auth.example/", ClientID: "my-app"}.AuthorizeURL(Inference, "c", "s")
	if !strings.HasPrefix(custom, "https://auth.example/v2/oauth2/authorize?") || !strings.Contains(custom, "client_id=my-app") {
		t.Errorf("custom client URL = %s", custom)
	}
}

func TestExchangeCodeSendsTheGrantAndPicksTheServiceToken(t *testing.T) {
	globus := newFakeGlobus(t, func(globusCall) (int, any) {
		return http.StatusOK, loginResponse("inference-access", "inference-refresh")
	})
	client := Client{AuthURL: globus.URL, Now: func() time.Time { return testNow }}
	tok, err := client.ExchangeCode(context.Background(), Inference, "the-code", "the-verifier")
	if err != nil {
		t.Fatal(err)
	}
	want := Token{
		Scope:        Inference.Scope,
		AccessToken:  "inference-access",
		RefreshToken: "inference-refresh",
		ExpiresAt:    testNow.Unix() + 172800,
	}
	if tok != want {
		t.Errorf("token = %+v, want %+v", tok, want)
	}
	calls := globus.recorded()
	if len(calls) != 1 || calls[0].Path != "/v2/oauth2/token" {
		t.Fatalf("calls = %+v", calls)
	}
	form := calls[0].Form
	for key, value := range map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     DefaultClientID,
		"code":          "the-code",
		"code_verifier": "the-verifier",
		"redirect_uri":  RedirectURI,
	} {
		if got := form.Get(key); got != value {
			t.Errorf("form %s = %q, want %q", key, got, value)
		}
	}
	if form.Has("client_secret") {
		t.Error("a public client sent client_secret")
	}
}

func TestExchangeCodeFindsTheTokenByScopeAndRejectsOthers(t *testing.T) {
	respond := func(body any) Client {
		globus := newFakeGlobus(t, func(globusCall) (int, any) { return http.StatusOK, body })
		return Client{AuthURL: globus.URL, Now: func() time.Time { return testNow }}
	}
	byScope := inferenceTokenJSON("by-scope", "r")
	delete(byScope, "resource_server")
	byScope["scope"] = "openid " + Inference.Scope
	tok, err := respond(byScope).ExchangeCode(context.Background(), Inference, "c", "v")
	if err != nil || tok.AccessToken != "by-scope" {
		t.Errorf("by scope: token %+v, err %v", tok, err)
	}

	other := map[string]any{"access_token": "x", "expires_in": 60, "resource_server": "transfer.api.globus.org", "scope": "urn:transfer"}
	if _, err := respond(other).ExchangeCode(context.Background(), Inference, "c", "v"); err == nil || !strings.Contains(err.Error(), "no token for resource server") {
		t.Errorf("other resource server: err = %v", err)
	}

	noExpiry := inferenceTokenJSON("a", "r")
	delete(noExpiry, "expires_in")
	if _, err := respond(noExpiry).ExchangeCode(context.Background(), Inference, "c", "v"); err == nil || !strings.Contains(err.Error(), "expires_in") {
		t.Errorf("no expires_in: err = %v", err)
	}
}

func TestExchangeCodeReportsGlobusErrors(t *testing.T) {
	globus := newFakeGlobus(t, func(globusCall) (int, any) {
		return http.StatusUnauthorized, map[string]any{"error": "invalid_grant", "error_description": "code expired"}
	})
	_, err := Client{AuthURL: globus.URL}.ExchangeCode(context.Background(), Inference, "c", "v")
	var gerr *GlobusError
	if !errors.As(err, &gerr) || gerr.StatusCode != http.StatusUnauthorized || gerr.Code != "invalid_grant" {
		t.Fatalf("err = %v", err)
	}
	if want := "exchange authorization code: Globus Auth answered HTTP 401: invalid_grant: code expired"; err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
	if errors.Is(err, ErrLoginRequired) {
		t.Error("a failed code exchange is not a missing login")
	}
}

func TestGlobusErrorReadsTheErrorsArrayAndPlainBodies(t *testing.T) {
	e := newGlobusError(http.StatusForbidden, []byte(`{"errors":[{"code":"FORBIDDEN","detail":"policy not met"}]}`))
	if e.Code != "FORBIDDEN" || e.Description != "policy not met" {
		t.Errorf("errors array: %+v", e)
	}
	plain := newGlobusError(http.StatusBadGateway, []byte("upstream down\n"))
	if got := plain.Error(); got != "Globus Auth answered HTTP 502: upstream down" {
		t.Errorf("plain body: %q", got)
	}
}

func TestRefreshSendsTheRefreshGrant(t *testing.T) {
	globus := newFakeGlobus(t, func(globusCall) (int, any) {
		return http.StatusOK, inferenceTokenJSON("new-access", "")
	})
	client := Client{AuthURL: globus.URL, ClientID: "app", Now: func() time.Time { return testNow }}
	tok, err := client.Refresh(context.Background(), Inference, "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "new-access" || tok.RefreshToken != "old-refresh" {
		t.Errorf("token = %+v: want the new access token and the old refresh token", tok)
	}
	form := globus.recorded()[0].Form
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "old-refresh" || form.Get("client_id") != "app" {
		t.Errorf("form = %v", form)
	}
}

func TestRefreshTakesARotatedRefreshToken(t *testing.T) {
	globus := newFakeGlobus(t, func(globusCall) (int, any) {
		return http.StatusOK, inferenceTokenJSON("new-access", "new-refresh")
	})
	tok, err := Client{AuthURL: globus.URL}.Refresh(context.Background(), Inference, "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken != "new-refresh" {
		t.Errorf("refresh token = %q, want new-refresh", tok.RefreshToken)
	}
}

func TestRefreshRefusedRequiresLogin(t *testing.T) {
	globus := newFakeGlobus(t, func(globusCall) (int, any) {
		return http.StatusBadRequest, map[string]any{"error": "invalid_grant"}
	})
	_, err := Client{AuthURL: globus.URL}.Refresh(context.Background(), Inference, "stale")
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v, want ErrLoginRequired", err)
	}

	down := newFakeGlobus(t, func(globusCall) (int, any) {
		return http.StatusServiceUnavailable, map[string]any{"error": "unavailable"}
	})
	_, err = Client{AuthURL: down.URL}.Refresh(context.Background(), Inference, "fine")
	if err == nil || errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v, want an error that is not ErrLoginRequired", err)
	}
}

func TestLoginSavesTheCacheAndTheKeyFile(t *testing.T) {
	globus := newFakeGlobus(t, func(globusCall) (int, any) {
		return http.StatusOK, loginResponse("inference-access", "inference-refresh")
	})
	store := testStore(t, globus.URL)
	var out bytes.Buffer
	opened := ""
	tok, err := store.Login(context.Background(), LoginIO{
		In:          strings.NewReader("  the-code \n"),
		Out:         &out,
		OpenBrowser: func(u string) error { opened = u; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok.AuthenticatedAt != testNow.Unix() {
		t.Errorf("AuthenticatedAt = %d, want %d", tok.AuthenticatedAt, testNow.Unix())
	}

	printed := ""
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, globus.URL+"/v2/oauth2/authorize?") {
			printed = line
		}
	}
	if printed == "" {
		t.Fatalf("login URL not printed:\n%s", out.String())
	}
	if opened != printed {
		t.Errorf("browser opened %q, printed %q", opened, printed)
	}
	if !strings.Contains(out.String(), "Paste the authorization code") {
		t.Errorf("no prompt:\n%s", out.String())
	}
	u, _ := url.Parse(printed)
	form := globus.recorded()[0].Form
	if form.Get("code") != "the-code" {
		t.Errorf("code = %q, want the pasted code trimmed", form.Get("code"))
	}
	if challengeS256(form.Get("code_verifier")) != u.Query().Get("code_challenge") {
		t.Error("the verifier sent does not match the challenge in the login URL")
	}

	key, err := auth.ReadKeyFile(store.KeyFilePath)
	if err != nil || key != "inference-access" {
		t.Errorf("key file = %q, %v", key, err)
	}
	cache := readJSON(t, store.CachePath)
	if cache["app_name"] != AppName || cache["client_id"] != DefaultClientID {
		t.Errorf("cache header = %v", cache)
	}
	entry := cachedEntry(t, store.CachePath, Inference.ResourceServer)
	if entry["access_token"] != "inference-access" || entry["refresh_token"] != "inference-refresh" ||
		entry["expires_at"] != float64(testNow.Unix()+172800) || entry["authenticated_at"] != float64(testNow.Unix()) {
		t.Errorf("cache entry = %v", entry)
	}
	assertMode(t, filepath.Dir(store.CachePath), 0o700)
	assertMode(t, store.CachePath, 0o600)
	assertMode(t, store.KeyFilePath, 0o600)
	assertNoTempFiles(t, filepath.Dir(store.CachePath))
}

func TestLoginRecordsACustomClient(t *testing.T) {
	globus := newFakeGlobus(t, func(globusCall) (int, any) {
		return http.StatusOK, inferenceTokenJSON("a", "r")
	})
	store := testStore(t, globus.URL)
	store.Client.ClientID = "my-app"
	if _, err := store.Login(context.Background(), LoginIO{In: strings.NewReader("c\n"), Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if got := readJSON(t, store.CachePath)["client_id"]; got != "my-app" {
		t.Errorf("client_id = %v, want my-app", got)
	}
	if got := globus.recorded()[0].Form.Get("client_id"); got != "my-app" {
		t.Errorf("exchange client_id = %q", got)
	}
}

func TestLoginWithoutACodeSendsNothing(t *testing.T) {
	globus := newFakeGlobus(t, func(globusCall) (int, any) { return http.StatusOK, nil })
	store := testStore(t, globus.URL)
	_, err := store.Login(context.Background(), LoginIO{In: strings.NewReader("\n"), Out: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "no authorization code") {
		t.Fatalf("err = %v", err)
	}
	if n := len(globus.recorded()); n != 0 {
		t.Errorf("%d requests sent", n)
	}
	if _, err := os.Stat(store.CachePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("cache written: %v", err)
	}
}

func TestValidUsesTheCachedTokenWhileEnoughIsLeft(t *testing.T) {
	globus := newFakeGlobus(t, func(globusCall) (int, any) {
		t.Error("Globus called with time left on the token")
		return http.StatusInternalServerError, nil
	})
	store := testStore(t, globus.URL)
	writeCache(t, store.CachePath, map[string]any{
		"by_resource_server": map[string]any{
			Inference.ResourceServer: map[string]any{"access_token": "cached", "refresh_token": "r", "expires_at": testNow.Add(2 * time.Hour).Unix()},
		},
	})
	tok, refreshed, err := store.Valid(context.Background(), time.Hour)
	if err != nil || refreshed || tok.AccessToken != "cached" {
		t.Fatalf("token %+v, refreshed %v, err %v", tok, refreshed, err)
	}
	if key, _ := auth.ReadKeyFile(store.KeyFilePath); key != "cached" {
		t.Errorf("key file = %q, want the cached token", key)
	}

	// A key file that already holds the token keeps its identity, which is
	// what apiproxy watches; one that holds something else is rewritten.
	before, err := os.Stat(store.KeyFilePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Valid(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if after, err := os.Stat(store.KeyFilePath); err != nil || !os.SameFile(before, after) {
		t.Errorf("unchanged key file was replaced (err %v)", err)
	}
	if err := os.WriteFile(store.KeyFilePath, []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Valid(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if key, _ := auth.ReadKeyFile(store.KeyFilePath); key != "cached" {
		t.Errorf("key file = %q after Valid, want the cached token back", key)
	}
}

func TestValidRefreshesWhenLittleIsLeft(t *testing.T) {
	globus := newFakeGlobus(t, func(globusCall) (int, any) {
		return http.StatusOK, inferenceTokenJSON("fresh", "")
	})
	store := testStore(t, globus.URL)
	store.Client.ClientID = "flag-client"
	writeCache(t, store.CachePath, map[string]any{
		"client_id": "login-client",
		"by_resource_server": map[string]any{
			Inference.ResourceServer: map[string]any{
				"access_token": "stale", "refresh_token": "r1",
				"expires_at": testNow.Add(30 * time.Minute).Unix(), "authenticated_at": testNow.Add(-48 * time.Hour).Unix(),
			},
		},
	})
	if err := os.WriteFile(store.KeyFilePath, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, refreshed, err := store.Valid(context.Background(), time.Hour)
	if err != nil || !refreshed || tok.AccessToken != "fresh" || tok.RefreshToken != "r1" {
		t.Fatalf("token %+v, refreshed %v, err %v", tok, refreshed, err)
	}
	if got := globus.recorded()[0].Form.Get("client_id"); got != "login-client" {
		t.Errorf("refresh client_id = %q, want the client the cache records", got)
	}
	entry := cachedEntry(t, store.CachePath, Inference.ResourceServer)
	if entry["access_token"] != "fresh" || entry["refresh_token"] != "r1" || entry["authenticated_at"] != float64(testNow.Add(-48*time.Hour).Unix()) {
		t.Errorf("cache entry = %v", entry)
	}
	if key, _ := auth.ReadKeyFile(store.KeyFilePath); key != "fresh" {
		t.Errorf("key file = %q, want the refreshed token", key)
	}
	assertNoTempFiles(t, filepath.Dir(store.CachePath))
}

func TestValidWithoutALoginRequiresOne(t *testing.T) {
	store := testStore(t, "http://127.0.0.1:1")
	if _, _, err := store.Valid(context.Background(), time.Hour); !errors.Is(err, ErrLoginRequired) {
		t.Errorf("no cache: err = %v", err)
	}
	writeCache(t, store.CachePath, map[string]any{
		"by_resource_server": map[string]any{
			Inference.ResourceServer: map[string]any{"access_token": "old", "expires_at": testNow.Add(-time.Hour).Unix()},
		},
	})
	if _, _, err := store.Valid(context.Background(), time.Hour); !errors.Is(err, ErrLoginRequired) {
		t.Errorf("no refresh token: err = %v", err)
	}
	if _, err := os.Stat(store.KeyFilePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("key file written without a valid token: %v", err)
	}
}

func TestValidRefreshesOnceForConcurrentCallers(t *testing.T) {
	globus := newFakeGlobus(t, func(globusCall) (int, any) {
		time.Sleep(20 * time.Millisecond)
		return http.StatusOK, inferenceTokenJSON("fresh", "")
	})
	store := testStore(t, globus.URL)
	writeCache(t, store.CachePath, map[string]any{
		"by_resource_server": map[string]any{
			Inference.ResourceServer: map[string]any{"access_token": "stale", "refresh_token": "r", "expires_at": testNow.Unix()},
		},
	})
	const callers = 4
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, _, err := store.Valid(context.Background(), time.Hour)
			if err == nil && tok.AccessToken != "fresh" {
				err = fmt.Errorf("token = %q", tok.AccessToken)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if n := len(globus.recorded()); n != 1 {
		t.Errorf("%d refreshes, want 1", n)
	}
}

func TestSaveKeepsWhatOtherToolsWrote(t *testing.T) {
	globus := newFakeGlobus(t, func(globusCall) (int, any) {
		return http.StatusOK, inferenceTokenJSON("fresh", "")
	})
	store := testStore(t, globus.URL)
	iri := map[string]any{"scope": "iri-scope", "access_token": "iri", "refresh_token": "iri-r", "expires_at": float64(123), "expires_at_epoch": float64(123)}
	writeCache(t, store.CachePath, map[string]any{
		"app_name":  "alcf_tokens",
		"client_id": DefaultClientID,
		"note":      "kept",
		"by_resource_server": map[string]any{
			"6be511f6-a071-471f-9bc0-02a0d0836723": iri,
			Inference.ResourceServer:               map[string]any{"access_token": "stale", "refresh_token": "r", "expires_at": float64(0)},
		},
	})
	if _, _, err := store.Valid(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	cache := readJSON(t, store.CachePath)
	if cache["app_name"] != "alcf_tokens" || cache["note"] != "kept" {
		t.Errorf("top level fields = %v", cache)
	}
	got := cachedEntry(t, store.CachePath, "6be511f6-a071-471f-9bc0-02a0d0836723")
	for key, value := range iri {
		if got[key] != value {
			t.Errorf("iri %s = %v, want %v", key, got[key], value)
		}
	}
}

func TestReadCacheRejectsAFileThatIsNotACache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte("sk-not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCache(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("err = %v, want a parse error naming the file", err)
	}
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := readCache(path); err != nil || len(c.servers) != 0 {
		t.Errorf("empty file: %v, %v", c, err)
	}
}

func TestLogoutRevokesBothTokensAndRemovesTheFiles(t *testing.T) {
	globus := newFakeGlobus(t, func(globusCall) (int, any) { return http.StatusOK, map[string]any{"active": false} })
	store := testStore(t, globus.URL)
	writeCache(t, store.CachePath, map[string]any{
		"client_id": "login-client",
		"by_resource_server": map[string]any{
			Inference.ResourceServer: map[string]any{"access_token": "a", "refresh_token": "r", "expires_at": testNow.Unix()},
		},
	})
	if err := os.WriteFile(store.KeyFilePath, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	var revoked []string
	for _, call := range globus.recorded() {
		if call.Path != "/v2/oauth2/token/revoke" || call.Form.Get("client_id") != "login-client" {
			t.Errorf("call = %+v", call)
		}
		revoked = append(revoked, call.Form.Get("token"))
	}
	if strings.Join(revoked, ",") != "r,a" {
		t.Errorf("revoked %v, want the refresh token and then the access token", revoked)
	}
	for _, path := range []string{store.CachePath, store.KeyFilePath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still there: %v", path, err)
		}
	}
}

func TestLogoutKeepsOtherServicesAndKeepsEverythingWhenRevokeFails(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	globus := newFakeGlobus(t, func(globusCall) (int, any) {
		if fail.Load() {
			return http.StatusServiceUnavailable, map[string]any{"error": "unavailable"}
		}
		return http.StatusOK, map[string]any{}
	})
	store := testStore(t, globus.URL)
	writeCache(t, store.CachePath, map[string]any{
		"by_resource_server": map[string]any{
			"other":                  map[string]any{"access_token": "o"},
			Inference.ResourceServer: map[string]any{"access_token": "a", "refresh_token": "r", "expires_at": testNow.Unix()},
		},
	})
	if err := os.WriteFile(store.KeyFilePath, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Logout(context.Background()); err == nil {
		t.Fatal("logout succeeded with Globus down")
	}
	cachedEntry(t, store.CachePath, Inference.ResourceServer)
	if _, err := os.Stat(store.KeyFilePath); err != nil {
		t.Errorf("key file removed after a failed revoke: %v", err)
	}

	fail.Store(false)
	if err := store.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	servers, _ := readJSON(t, store.CachePath)["by_resource_server"].(map[string]any)
	if _, ok := servers[Inference.ResourceServer]; ok || servers["other"] == nil {
		t.Errorf("servers after logout = %v", servers)
	}
	if _, err := os.Stat(store.KeyFilePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("key file still there: %v", err)
	}
}

func TestLoginAge(t *testing.T) {
	if _, ok := LoginAge(Token{}, testNow); ok {
		t.Error("a token without authenticated_at has an age")
	}
	age, ok := LoginAge(Token{AuthenticatedAt: testNow.Add(-72 * time.Hour).Unix()}, testNow)
	if !ok || age != 72*time.Hour {
		t.Errorf("age = %v, %v", age, ok)
	}
}

func TestGatewayWhoami(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/resource_server/whoami" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"detail":"Permission denied."}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"u1","name":"Ada","username":"ada@anl.gov","idp_name":"Argonne National Laboratory","user_group_uuids":[],"idp_id":"i","auth_service":"globus"}`))
	}))
	t.Cleanup(gateway.Close)
	g := Gateway{URL: gateway.URL + "/resource_server/"}

	user, err := g.Whoami(context.Background(), "tok")
	if err != nil || user != (User{ID: "u1", Name: "Ada", Username: "ada@anl.gov", IdPName: "Argonne National Laboratory"}) {
		t.Errorf("whoami = %+v, %v", user, err)
	}
	if _, err := g.Whoami(context.Background(), "other"); err == nil || !strings.Contains(err.Error(), "HTTP 403: Permission denied.") {
		t.Errorf("refused: err = %v", err)
	}
}

func TestBrowserCommand(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		goos string
		want string
	}{
		{"macOS", nil, "darwin", "open"},
		{"linux with a display", map[string]string{"DISPLAY": ":0"}, "linux", "xdg-open"},
		{"wayland", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, "freebsd", "xdg-open"},
		{"no display", nil, "linux", ""},
		{"ssh", map[string]string{"SSH_CONNECTION": "1 2 3 4"}, "darwin", ""},
		{"BROWSER wins over ssh", map[string]string{"BROWSER": "firefox --new-window:lynx", "SSH_TTY": "/dev/ttys001"}, "linux", "firefox --new-window"},
		{"blank BROWSER", map[string]string{"BROWSER": " "}, "darwin", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, args := browserCommand(func(k string) string { return tt.env[k] }, tt.goos)
			if got := strings.TrimSpace(name + " " + strings.Join(args, " ")); got != tt.want {
				t.Errorf("command = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultPathsAreUnderLmc(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cache, err := DefaultCachePath()
	if err != nil || cache != filepath.Join(home, ".lmc", "alcf", "tokens.json") {
		t.Errorf("cache = %q, %v", cache, err)
	}
	key, err := DefaultKeyFilePath()
	if err != nil || key != filepath.Join(home, ".lmc", "alcf", "inference.key") {
		t.Errorf("key file = %q, %v", key, err)
	}
}
