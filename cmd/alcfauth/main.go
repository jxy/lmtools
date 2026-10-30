// Command alcfauth logs in to the ALCF Inference Endpoints through Globus and
// keeps the access token in a key file for lmc -api-key-file.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"lmtools/internal/alcfauth"
	lmerrors "lmtools/internal/errors"
	"lmtools/internal/version"
	"net/http"
	"os"
	"strings"
	"time"
)

const usageText = `usage: alcfauth <command> [flags]

Log in to the ALCF Inference Endpoints through Globus and keep the access
token in a key file for lmc -api-key-file.

Commands:
  login    log in and write the token cache and the key file
  token    refresh the access token when needed, rewrite the key file, print it
  status   show how long the access token and the login have left
  logout   revoke the tokens and remove them

Run 'alcfauth <command> -h' for a command's flags.
'alcfauth -version' prints the version.
`

// env is what a run reads and writes. Tests point the URLs at fake servers.
type env struct {
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
	authURL     string
	gatewayURL  string
	httpClient  *http.Client
	now         func() time.Time
	openBrowser func(url string) error
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], env{
		stdin:       os.Stdin,
		stdout:      os.Stdout,
		stderr:      os.Stderr,
		now:         time.Now,
		openBrowser: alcfauth.OpenBrowser,
	}))
}

type command func(ctx context.Context, args []string, e env) error

var commands = map[string]command{
	"login":  runLogin,
	"token":  runToken,
	"status": runStatus,
	"logout": runLogout,
}

// errUsage reports a command line that was wrong and has been explained
// already.
var errUsage = errors.New("usage")

func run(ctx context.Context, args []string, e env) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, usageText)
		return 2
	}
	switch args[0] {
	case "-version", "--version":
		fmt.Fprintln(e.stdout, version.String("alcfauth"))
		return 0
	case "-h", "-help", "--help", "help":
		fmt.Fprint(e.stdout, usageText)
		return 0
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(e.stderr, "alcfauth: unknown command %q\n\n%s", args[0], usageText)
		return 2
	}
	err := cmd(ctx, args[1:], e)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		return 2
	case errors.Is(err, alcfauth.ErrLoginRequired):
		fmt.Fprintf(e.stderr, "alcfauth: %v\nRun 'alcfauth login' to log in.\n", err)
		return 1
	default:
		fmt.Fprintf(e.stderr, "alcfauth: %v\n", err)
		return 1
	}
}

// options are the flags every command takes.
type options struct {
	cache   string
	keyFile string
}

func newFlagSet(name, synopsis string, e env, o *options) *flag.FlagSet {
	fs := flag.NewFlagSet("alcfauth "+name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.StringVar(&o.cache, "cache", "", "token cache `file` (default ~/.lmc/alcf/tokens.json)")
	fs.StringVar(&o.keyFile, "key-file", "", "`file` that holds the access token for lmc -api-key-file (default ~/.lmc/alcf/inference.key)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: alcfauth %s\n\n", synopsis)
		fs.PrintDefaults()
	}
	return fs
}

// parse reads flags on both sides of any positional arguments, so a stray
// word is reported rather than hiding the flags after it.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, errUsage
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func noArguments(fs *flag.FlagSet, args []string) error {
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) > 0 {
		fmt.Fprintf(fs.Output(), "%s: unexpected argument %q\n", fs.Name(), positional[0])
		fs.Usage()
		return errUsage
	}
	return nil
}

func (o options) store(e env) (alcfauth.Store, error) {
	cache, keyFile := o.cache, o.keyFile
	var err error
	if cache == "" {
		if cache, err = alcfauth.DefaultCachePath(); err != nil {
			return alcfauth.Store{}, err
		}
	}
	if keyFile == "" {
		if keyFile, err = alcfauth.DefaultKeyFilePath(); err != nil {
			return alcfauth.Store{}, err
		}
	}
	return alcfauth.Store{
		CachePath:   cache,
		KeyFilePath: keyFile,
		Service:     alcfauth.Inference,
		Client:      alcfauth.Client{AuthURL: e.authURL, HTTP: e.httpClient, Now: e.now},
	}, nil
}

func (e env) gateway() alcfauth.Gateway {
	return alcfauth.Gateway{URL: e.gatewayURL, HTTP: e.httpClient}
}

func runLogin(ctx context.Context, args []string, e env) error {
	var o options
	fs := newFlagSet("login", "login [flags]", e, &o)
	clientID := fs.String("client-id", "", "Globus native app `id` to log in with (default "+alcfauth.DefaultClientID+", the alcf-tokens app)")
	noBrowser := fs.Bool("no-browser", false, "print the login URL without opening a browser")
	if err := noArguments(fs, args); err != nil {
		return err
	}
	store, err := o.store(e)
	if err != nil {
		return err
	}
	store.Client.ClientID = *clientID
	lio := alcfauth.LoginIO{In: e.stdin, Out: e.stderr}
	if !*noBrowser {
		lio.OpenBrowser = e.openBrowser
	}
	tok, err := store.Login(ctx, lio)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "\nLogged in to the ALCF Inference Service. The access token is valid until %s.\n", formatTime(tok.Expiry()))
	fmt.Fprintf(e.stdout, "Token cache: %s\nKey file:    %s\n\n", store.CachePath, store.KeyFilePath)
	fmt.Fprintf(e.stdout, "Use the key file with lmc, for example on Minerva:\n\n"+
		"  echo 'Hello' | lmc -provider openai \\\n"+
		"      -provider-url %s/minerva/api/v1 \\\n"+
		"      -api-key-file %s \\\n"+
		"      -model mistral-large-4-nvfp4\n\n"+
		"Run 'alcfauth token -q' before a session to refresh the key file.\n",
		alcfauth.DefaultGatewayURL, store.KeyFilePath)
	return nil
}

func runToken(ctx context.Context, args []string, e env) error {
	var o options
	fs := newFlagSet("token", "token [flags]", e, &o)
	minValid := fs.Duration("min-valid", time.Hour, "refresh the access token when less than this `duration` is left")
	quiet := fs.Bool("q", false, "write the key file without printing the token")
	if err := noArguments(fs, args); err != nil {
		return err
	}
	if *minValid < 0 {
		fmt.Fprintf(e.stderr, "%s: -min-valid must not be negative\n", fs.Name())
		return errUsage
	}
	store, err := o.store(e)
	if err != nil {
		return err
	}
	tok, _, err := store.Valid(ctx, *minValid)
	if err != nil {
		return err
	}
	warnLoginAge(e, tok)
	if !*quiet {
		fmt.Fprintln(e.stdout, tok.AccessToken)
	}
	return nil
}

func runStatus(ctx context.Context, args []string, e env) error {
	var o options
	fs := newFlagSet("status", "status [flags]", e, &o)
	check := fs.Bool("check", false, "ask the gateway whether it accepts the token, refreshing an expired one first")
	if err := noArguments(fs, args); err != nil {
		return err
	}
	store, err := o.store(e)
	if err != nil {
		return err
	}
	tok, err := store.Load()
	if err != nil {
		return err
	}
	now := e.now()
	fmt.Fprintf(e.stdout, "Token cache:  %s\n", store.CachePath)
	fmt.Fprintf(e.stdout, "Key file:     %s%s\n", store.KeyFilePath, keyFileNote(store.KeyFilePath, tok.AccessToken))
	if left := tok.Expiry().Sub(now); left > 0 {
		fmt.Fprintf(e.stdout, "Access token: valid for %s, until %s\n", formatDuration(left), formatTime(tok.Expiry()))
	} else {
		fmt.Fprintf(e.stdout, "Access token: expired %s ago; 'alcfauth token' refreshes it\n", formatDuration(-left))
	}
	if age, ok := alcfauth.LoginAge(tok, now); ok {
		fmt.Fprintf(e.stdout, "Login:        %s, %s ago; ALCF asks for a new login every 30 days\n",
			formatTime(time.Unix(tok.AuthenticatedAt, 0)), formatDays(age))
	} else {
		fmt.Fprintln(e.stdout, "Login:        time not recorded")
	}
	warnLoginAge(e, tok)
	if !*check {
		return nil
	}
	valid, refreshed, err := store.Valid(ctx, time.Minute)
	if err != nil {
		return err
	}
	if refreshed {
		fmt.Fprintf(e.stdout, "Refreshed:    the access token is valid until %s\n", formatTime(valid.Expiry()))
	}
	user, err := e.gateway().Whoami(ctx, valid.AccessToken)
	if err != nil {
		var herr *lmerrors.HTTPError
		if errors.As(err, &herr) && (herr.StatusCode == http.StatusUnauthorized || herr.StatusCode == http.StatusForbidden) {
			return fmt.Errorf("the gateway refused the token: %w\nIf the last login is 30 days old or more, run 'alcfauth login'", err)
		}
		return fmt.Errorf("check the token with the gateway: %w", err)
	}
	fmt.Fprintf(e.stdout, "Gateway:      accepts the token for %s (%s) from %s\n", user.Name, user.Username, user.IdPName)
	return nil
}

// keyFileNote says when the key file does not hold the cached access token.
func keyFileNote(path, accessToken string) string {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return " (missing; 'alcfauth token' writes it)"
	case err != nil:
		return fmt.Sprintf(" (unreadable: %v)", err)
	case strings.TrimSpace(string(data)) != accessToken:
		return " (holds another token; 'alcfauth token' rewrites it)"
	default:
		return ""
	}
}

func runLogout(ctx context.Context, args []string, e env) error {
	var o options
	fs := newFlagSet("logout", "logout [flags]", e, &o)
	if err := noArguments(fs, args); err != nil {
		return err
	}
	store, err := o.store(e)
	if err != nil {
		return err
	}
	if err := store.Logout(ctx); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Revoked the tokens and removed them from %s and %s.\n", store.CachePath, store.KeyFilePath)
	fmt.Fprintln(e.stdout, "The gateway can take up to ten minutes to notice the revocation.")
	return nil
}

// warnLoginAge tells the user when ALCF's 30 day login period is about to end
// or has ended. The gateway enforces it; a refresh still works.
func warnLoginAge(e env, tok alcfauth.Token) {
	age, ok := alcfauth.LoginAge(tok, e.now())
	if !ok || age < alcfauth.LoginWarnAge {
		return
	}
	days := int(age / (24 * time.Hour))
	if age >= alcfauth.LoginLifetime {
		fmt.Fprintf(e.stderr, "alcfauth: warning: the last login was %d days ago, and ALCF asks for a new one every 30 days. Run 'alcfauth login' if the gateway refuses the token.\n", days)
		return
	}
	fmt.Fprintf(e.stderr, "alcfauth: warning: the last login was %d days ago, and ALCF asks for a new one every 30 days. Run 'alcfauth login' soon.\n", days)
}

func formatDays(d time.Duration) string {
	if n := int(d / (24 * time.Hour)); n != 1 {
		return fmt.Sprintf("%d days", n)
	}
	return "1 day"
}

func formatTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04 MST")
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h := int(d / time.Hour)
	m := int(d % time.Hour / time.Minute)
	if h == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%02dm", h, m)
}
