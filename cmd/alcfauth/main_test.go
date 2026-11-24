package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/alcfauth"
	"lmtools/internal/auth"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeALCF serves Globus Auth's token and revoke endpoints and the gateway's
// whoami route. Each refresh hands out the next numbered token.
type fakeALCF struct {
	*httptest.Server
	mu            sync.Mutex
	exchanges     int
	refreshes     int
	revoked       []string
	gatewayStatus int
	// refreshFailures answers that many refreshes with refreshStatus.
	refreshFailures int
	refreshStatus   int
}

func newFakeALCF(t *testing.T) *fakeALCF {
	t.Helper()
	f := &fakeALCF{gatewayStatus: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeALCF) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	token := func(access, refresh string) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": access, "refresh_token": refresh, "expires_in": 172800,
			"resource_server": alcfauth.Inference.ResourceServer, "scope": alcfauth.Inference.Scope,
		})
	}
	switch r.URL.Path {
	case "/v2/oauth2/token":
		_ = r.ParseForm()
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			f.exchanges++
			token("access-0", "refresh-0")
		case "refresh_token":
			if f.refreshFailures > 0 {
				f.refreshFailures--
				w.WriteHeader(f.refreshStatus)
				if f.refreshStatus == http.StatusBadRequest {
					_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refused by the test"}`))
				} else {
					_, _ = w.Write([]byte(`{"error":"server_error"}`))
				}
				return
			}
			f.refreshes++
			token(fmt.Sprintf("access-%d", f.refreshes), "")
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	case "/v2/oauth2/token/revoke":
		_ = r.ParseForm()
		f.revoked = append(f.revoked, r.PostForm.Get("token"))
		_, _ = w.Write([]byte(`{}`))
	case "/resource_server/whoami":
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.gatewayStatus != http.StatusOK {
			w.WriteHeader(f.gatewayStatus)
			_, _ = w.Write([]byte(`{"detail":"Permission denied."}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"u1","name":"Ada Lovelace","username":"ada@anl.gov","idp_name":"Argonne National Laboratory"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeALCF) counts() (exchanges, refreshes int, revoked []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchanges, f.refreshes, append([]string(nil), f.revoked...)
}

// cli runs alcfauth in process with HOME in a temporary directory, so the
// default paths stay inside the test.
type cli struct {
	t       *testing.T
	server  *fakeALCF
	home    string
	now     time.Time
	opened  []string
	nextIn  string
	browser func(string) error
	wait    func(ctx context.Context, d time.Duration) error
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := &cli{t: t, server: newFakeALCF(t), home: home, now: time.Unix(1_793_377_319, 0)}
	c.browser = func(u string) error { c.opened = append(c.opened, u); return nil }
	return c
}

func (c *cli) run(args ...string) (code int, stdout, stderr string) {
	c.t.Helper()
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, env{
		stdin:       strings.NewReader(c.nextIn),
		stdout:      &out,
		stderr:      &errOut,
		authURL:     c.server.URL,
		gatewayURL:  c.server.URL + "/resource_server",
		now:         func() time.Time { return c.now },
		openBrowser: c.browser,
		wait:        c.wait,
	})
	return code, out.String(), errOut.String()
}

func (c *cli) keyFile() string { return filepath.Join(c.home, ".lmc", "alcf", "inference.key") }
func (c *cli) cache() string   { return filepath.Join(c.home, ".lmc", "alcf", "tokens.json") }

func (c *cli) key() string {
	c.t.Helper()
	key, err := auth.ReadKeyFile(c.keyFile())
	if err != nil {
		c.t.Fatalf("read key file: %v", err)
	}
	return key
}

func TestLoginTokenStatusLogout(t *testing.T) {
	c := newCLI(t)

	c.nextIn = "the-code\n"
	code, stdout, stderr := c.run("login")
	if code != 0 {
		t.Fatalf("login exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, c.server.URL+"/v2/oauth2/authorize?") || !strings.Contains(stderr, "Paste the authorization code") {
		t.Errorf("login stderr lacks the URL or the prompt:\n%s", stderr)
	}
	if len(c.opened) != 1 {
		t.Errorf("browser opened %d times, want 1", len(c.opened))
	}
	for _, want := range []string{c.cache(), c.keyFile(), "-provider-url https://inference-api.alcf.anl.gov/resource_server/minerva/api/v1", "-api-key-file " + c.keyFile()} {
		if !strings.Contains(stdout, want) {
			t.Errorf("login stdout lacks %q:\n%s", want, stdout)
		}
	}
	if got := c.key(); got != "access-0" {
		t.Errorf("key after login = %q", got)
	}

	code, stdout, stderr = c.run("token")
	if code != 0 || stdout != "access-0\n" || stderr != "" {
		t.Errorf("token: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, refreshes, _ := c.server.counts(); refreshes != 0 {
		t.Errorf("token refreshed a fresh token")
	}

	c.now = c.now.Add(47*time.Hour + 30*time.Minute)
	code, stdout, _ = c.run("token", "-quiet")
	if code != 0 || stdout != "" {
		t.Errorf("token -quiet: exit %d, stdout %q", code, stdout)
	}
	if got := c.key(); got != "access-1" {
		t.Errorf("key after refresh = %q, want access-1", got)
	}

	code, stdout, stderr = c.run("status", "-check")
	if code != 0 {
		t.Fatalf("status exit %d: %s", code, stderr)
	}
	for _, want := range []string{"Token cache:  " + c.cache(), "Key file:     " + c.keyFile() + "\n", "valid for 48h00m", "1 day ago", "Gateway:      accepts the token for Ada Lovelace (ada@anl.gov) from Argonne National Laboratory"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status lacks %q:\n%s", want, stdout)
		}
	}

	code, stdout, stderr = c.run("logout")
	if code != 0 || !strings.Contains(stdout, "Revoked the tokens") {
		t.Fatalf("logout: exit %d\n%s%s", code, stdout, stderr)
	}
	if _, _, revoked := c.server.counts(); strings.Join(revoked, ",") != "refresh-0,access-1" {
		t.Errorf("revoked %v", revoked)
	}
	for _, path := range []string{c.cache(), c.keyFile()} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left after logout: %v", path, err)
		}
	}

	code, stdout, stderr = c.run("token")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "Run 'alcfauth login'") {
		t.Errorf("token after logout: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestLoginNoBrowserAndCustomPaths(t *testing.T) {
	c := newCLI(t)
	dir := t.TempDir()
	cache, key := filepath.Join(dir, "c", "t.json"), filepath.Join(dir, "k", "minerva.key")
	c.nextIn = "code\n"
	if code, _, stderr := c.run("login", "-no-browser", "-cache", cache, "-key-file", key, "-client-id", "my-app"); code != 0 {
		t.Fatalf("login: %s", stderr)
	}
	if len(c.opened) != 0 {
		t.Errorf("browser opened with -no-browser")
	}
	if got, _ := auth.ReadKeyFile(key); got != "access-0" {
		t.Errorf("custom key file = %q", got)
	}
	if _, err := os.Stat(c.keyFile()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("default key file written: %v", err)
	}
	data, _ := os.ReadFile(cache)
	if !strings.Contains(string(data), `"client_id": "my-app"`) {
		t.Errorf("cache:\n%s", data)
	}
}

func TestStatusReportsExpiryAndTheKeyFile(t *testing.T) {
	c := newCLI(t)
	c.nextIn = "code\n"
	if code, _, stderr := c.run("login"); code != 0 {
		t.Fatal(stderr)
	}
	if err := os.Remove(c.keyFile()); err != nil {
		t.Fatal(err)
	}
	c.now = c.now.Add(50 * time.Hour)
	code, stdout, stderr := c.run("status")
	if code != 0 {
		t.Fatal(stderr)
	}
	if !strings.Contains(stdout, "(missing; 'alcfauth token' writes it)") || !strings.Contains(stdout, "expired 2h00m ago") {
		t.Errorf("status:\n%s", stdout)
	}
	if _, refreshes, _ := c.server.counts(); refreshes != 0 {
		t.Error("status without -check refreshed the token")
	}

	if err := os.WriteFile(c.keyFile(), []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stdout, _ := c.run("status"); !strings.Contains(stdout, "(holds another token") {
		t.Errorf("status with a stale key file:\n%s", stdout)
	}
}

func TestStatusCheckExplainsARefusal(t *testing.T) {
	c := newCLI(t)
	c.nextIn = "code\n"
	if code, _, stderr := c.run("login"); code != 0 {
		t.Fatal(stderr)
	}
	c.server.mu.Lock()
	c.server.gatewayStatus = http.StatusForbidden
	c.server.mu.Unlock()
	code, _, stderr := c.run("status", "-check")
	if code != 1 || !strings.Contains(stderr, "the gateway refused the token") || !strings.Contains(stderr, "HTTP 403: Permission denied.") || !strings.Contains(stderr, "run 'alcfauth login'") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}

func TestTokenWarnsAboutAnOldLogin(t *testing.T) {
	c := newCLI(t)
	c.nextIn = "code\n"
	if code, _, stderr := c.run("login"); code != 0 {
		t.Fatal(stderr)
	}
	c.now = c.now.Add(29*24*time.Hour + time.Hour)
	code, stdout, stderr := c.run("token")
	if code != 0 || !strings.HasPrefix(stdout, "access-") || strings.Count(stdout, "\n") != 1 {
		t.Errorf("token: exit %d, stdout %q", code, stdout)
	}
	if !strings.Contains(stderr, "29 days ago") || !strings.Contains(stderr, "Run 'alcfauth login' soon") {
		t.Errorf("no warning before the 30 days end:\n%s", stderr)
	}
	c.now = c.now.Add(2 * 24 * time.Hour)
	if _, _, stderr := c.run("token"); !strings.Contains(stderr, "31 days ago") || !strings.Contains(stderr, "if the gateway refuses the token") {
		t.Errorf("no warning after the 30 days:\n%s", stderr)
	}
	c.now = time.Unix(1_793_377_319, 0).Add(28 * 24 * time.Hour)
	if _, _, stderr := c.run("token"); stderr != "" {
		t.Errorf("warning at 28 days: %s", stderr)
	}
}

// watchUntil makes the CLI's waits advance its clock and records them, and
// ends the watch once done reports true.
func (c *cli) watchUntil(done func() bool) *[]time.Duration {
	var waits []time.Duration
	c.wait = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		c.now = c.now.Add(d)
		if done() {
			return context.Canceled
		}
		return ctx.Err()
	}
	return &waits
}

func TestTokenWatchRefreshesBeforeTheMargin(t *testing.T) {
	c := newCLI(t)
	c.nextIn = "code\n"
	if code, _, stderr := c.run("login"); code != 0 {
		t.Fatal(stderr)
	}
	login := c.now
	waits := c.watchUntil(func() bool {
		_, refreshes, _ := c.server.counts()
		return refreshes == 2
	})
	code, stdout, stderr := c.run("token", "-watch")
	if code != 0 || stdout != "" {
		t.Fatalf("token -watch: exit %d, stdout %q, stderr:\n%s", code, stdout, stderr)
	}
	if got := c.key(); got != "access-2" {
		t.Errorf("key file = %q, want access-2", got)
	}
	if !strings.Contains(stderr, "keeping "+c.keyFile()+" valid") || strings.Count(stderr, "refreshed the access token") != 2 {
		t.Errorf("stderr:\n%s", stderr)
	}
	for _, d := range *waits {
		if d < time.Minute || d > 10*time.Minute {
			t.Fatalf("wait of %s, want between 1m and 10m", d)
		}
	}
	// Each token lasts 48 hours and is refreshed with an hour left, so the
	// second refresh comes 94 hours after the login. The clock stops after the
	// wait that follows it, up to 10 minutes later.
	if elapsed := c.now.Sub(login); elapsed < 94*time.Hour || elapsed > 94*time.Hour+20*time.Minute {
		t.Errorf("watch ended %s after the login, want 94h to 94h20m", elapsed)
	}
}

func TestTokenWatchRetriesAFailedRefresh(t *testing.T) {
	c := newCLI(t)
	c.nextIn = "code\n"
	if code, _, stderr := c.run("login"); code != 0 {
		t.Fatal(stderr)
	}
	c.server.refreshFailures, c.server.refreshStatus = 2, http.StatusInternalServerError
	waits := c.watchUntil(func() bool {
		_, refreshes, _ := c.server.counts()
		return refreshes == 1
	})
	code, _, stderr := c.run("token", "-watch", "-min-valid", "49h")
	if code != 0 || strings.Count(stderr, "trying again") != 2 || !strings.Contains(stderr, "refreshed the access token") {
		t.Fatalf("token -watch: exit %d, stderr:\n%s", code, stderr)
	}
	if got := (*waits)[:2]; got[0] != time.Minute || got[1] != 2*time.Minute {
		t.Errorf("retry waits = %v, want 1m then 2m", got)
	}
}

func TestTokenWatchStopsWhenGlobusWantsALogin(t *testing.T) {
	c := newCLI(t)
	c.nextIn = "code\n"
	if code, _, stderr := c.run("login"); code != 0 {
		t.Fatal(stderr)
	}
	c.server.refreshFailures, c.server.refreshStatus = 1, http.StatusBadRequest
	waits := c.watchUntil(func() bool { return false })
	code, _, stderr := c.run("token", "-watch", "-min-valid", "49h")
	if code != 1 || !strings.Contains(stderr, "Run 'alcfauth login'") || len(*waits) != 0 {
		t.Errorf("token -watch: exit %d, %d waits, stderr:\n%s", code, len(*waits), stderr)
	}
}

func TestCommandLineErrors(t *testing.T) {
	c := newCLI(t)
	tests := []struct {
		args []string
		code int
		out  string
		err  string
	}{
		{nil, 2, "", "usage: alcfauth <command>"},
		{[]string{"frobnicate"}, 2, "", `unknown command "frobnicate"`},
		{[]string{"-h"}, 0, "Commands:", ""},
		{[]string{"-version"}, 0, "alcfauth version", ""},
		{[]string{"token", "-h"}, 0, "", "-min-valid"},
		{[]string{"token", "-bogus"}, 2, "", "flag provided but not defined"},
		{[]string{"token", "-min-valid", "-1h"}, 2, "", "must not be negative"},
		{[]string{"token", "extra"}, 2, "", `unexpected argument "extra"`},
		{[]string{"status"}, 1, "", "Run 'alcfauth login'"},
		{[]string{"token", "-cache", filepath.Join(c.home, "none.json")}, 1, "", "none.json"},
	}
	for _, tt := range tests {
		code, stdout, stderr := c.run(tt.args...)
		if code != tt.code || !strings.Contains(stdout, tt.out) || !strings.Contains(stderr, tt.err) {
			t.Errorf("%v: exit %d (want %d)\nstdout:\n%s\nstderr:\n%s", tt.args, code, tt.code, stdout, stderr)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                            "0m",
		59 * time.Second:             "1m",
		47*time.Hour + 5*time.Minute: "47h05m",
		72 * time.Hour:               "72h00m",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
