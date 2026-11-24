package alcfauth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/filelock"
	"os"
	"strings"
	"time"
)

const (
	// LoginLifetime is how long ALCF accepts a login before it asks for a
	// new one, whatever the refresh token says.
	LoginLifetime = 30 * 24 * time.Hour
	// LoginWarnAge is when token and status start to say so.
	LoginWarnAge = 29 * 24 * time.Hour

	defaultLockTimeout = 30 * time.Second
)

// Store keeps one service's tokens in a cache file and its access token in a
// key file.
type Store struct {
	CachePath   string
	KeyFilePath string
	Service     Service
	// Client is used for the login. Refresh and revocation use the client ID
	// recorded in the cache, since a refresh token belongs to the client it
	// was issued to.
	Client Client
	// LockTimeout bounds the wait for another process using the cache;
	// 30 seconds when zero.
	LockTimeout time.Duration
}

// withLock runs fn holding the lock next to the cache file, so two processes
// refreshing at once do not both spend the refresh token.
func (s Store) withLock(fn func() error) error {
	timeout := s.LockTimeout
	if timeout == 0 {
		timeout = defaultLockTimeout
	}
	return filelock.With(context.Background(), s.CachePath, timeout, fn)
}

func (s Store) cachedClient(c *cacheFile) Client {
	client := s.Client
	if id := c.str("client_id"); id != "" {
		client.ClientID = id
	}
	return client
}

// Load returns the cached token without refreshing it.
func (s Store) Load() (Token, error) {
	c, err := readCache(s.CachePath)
	if err != nil {
		return Token{}, err
	}
	tok, ok, err := c.token(s.Service.ResourceServer)
	if err != nil {
		return Token{}, err
	}
	if !ok {
		return Token{}, fmt.Errorf("%w: no %s token in %s", ErrLoginRequired, s.Service.Name, s.CachePath)
	}
	return tok, nil
}

// Valid returns an access token with at least minValid left, refreshing it
// when less remains, and writes it to the key file either way. refreshed
// reports whether it asked Globus for a new one.
func (s Store) Valid(ctx context.Context, minValid time.Duration) (tok Token, refreshed bool, err error) {
	err = s.withLock(func() error {
		c, err := readCache(s.CachePath)
		if err != nil {
			return err
		}
		current, ok, err := c.token(s.Service.ResourceServer)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: no %s token in %s", ErrLoginRequired, s.Service.Name, s.CachePath)
		}
		if current.AccessToken != "" && current.Expiry().Sub(s.Client.now()) >= minValid {
			tok = current
			return writeKeyFile(s.KeyFilePath, current.AccessToken)
		}
		if current.RefreshToken == "" {
			return fmt.Errorf("%w: the cached %s token has no refresh token", ErrLoginRequired, s.Service.Name)
		}
		next, err := s.cachedClient(c).Refresh(ctx, s.Service, current.RefreshToken)
		if err != nil {
			return err
		}
		next.AuthenticatedAt = current.AuthenticatedAt
		if err := s.save(c, next, ""); err != nil {
			return err
		}
		tok, refreshed = next, true
		return nil
	})
	return tok, refreshed, err
}

// save records tok in c, writes the cache, and then the key file. clientID,
// when set, replaces the cache's client.
func (s Store) save(c *cacheFile, tok Token, clientID string) error {
	if c.str("app_name") == "" {
		c.setStr("app_name", AppName)
	}
	if clientID != "" {
		c.setStr("client_id", clientID)
	}
	if err := c.setToken(s.Service.ResourceServer, tok); err != nil {
		return err
	}
	if err := c.write(s.CachePath); err != nil {
		return err
	}
	return writeKeyFile(s.KeyFilePath, tok.AccessToken)
}

// LoginIO is the terminal a login runs on.
type LoginIO struct {
	// In supplies the authorization code the user pastes.
	In io.Reader
	// Out receives the login URL and the prompt.
	Out io.Writer
	// OpenBrowser, when set, is asked to show the login URL.
	OpenBrowser func(url string) error
	// Random is the source for the PKCE verifier and state; crypto/rand when
	// nil.
	Random io.Reader
}

// Login runs the authorization code flow: it prints the login URL, reads the
// code Globus shows after the login, trades it for tokens, and saves them.
func (s Store) Login(ctx context.Context, lio LoginIO) (Token, error) {
	p, err := newPKCE(lio.Random)
	if err != nil {
		return Token{}, err
	}
	state, err := randomString(lio.Random)
	if err != nil {
		return Token{}, err
	}
	authorizeURL := s.Client.AuthorizeURL(s.Service, p.Challenge, state)
	fmt.Fprintf(lio.Out, "Open this URL in a browser and log in:\n\n%s\n\n", authorizeURL)
	if lio.OpenBrowser != nil {
		if err := lio.OpenBrowser(authorizeURL); err == nil {
			fmt.Fprintln(lio.Out, "A browser should open it now.")
		}
	}
	fmt.Fprint(lio.Out, "Paste the authorization code Globus shows: ")
	code, err := bufio.NewReader(lio.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return Token{}, fmt.Errorf("read authorization code: %w", err)
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return Token{}, errors.New("no authorization code given")
	}
	tok, err := s.Client.ExchangeCode(ctx, s.Service, code, p.Verifier)
	if err != nil {
		return Token{}, err
	}
	tok.AuthenticatedAt = s.Client.now().Unix()
	err = s.withLock(func() error {
		c, err := readCache(s.CachePath)
		if err != nil {
			return err
		}
		return s.save(c, tok, s.Client.clientID())
	})
	if err != nil {
		return Token{}, err
	}
	return tok, nil
}

// Logout revokes the service's tokens at Globus, removes them from the cache,
// and deletes the key file. The cache file goes too once nothing is left in
// it. When a revocation fails, nothing is removed, so the command can be run
// again.
func (s Store) Logout(ctx context.Context) error {
	return s.withLock(func() error {
		c, err := readCache(s.CachePath)
		if err != nil {
			return err
		}
		tok, ok, err := c.token(s.Service.ResourceServer)
		if err != nil {
			return err
		}
		if ok {
			client := s.cachedClient(c)
			for _, t := range []string{tok.RefreshToken, tok.AccessToken} {
				if t == "" {
					continue
				}
				if err := client.Revoke(ctx, t); err != nil {
					return err
				}
			}
			c.deleteToken(s.Service.ResourceServer)
		}
		if len(c.servers) == 0 {
			if err := removeIfExists(s.CachePath); err != nil {
				return err
			}
		} else if err := c.write(s.CachePath); err != nil {
			return err
		}
		return removeIfExists(s.KeyFilePath)
	})
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// LoginAge is how long ago the login behind tok ran; false when the cache
// does not say, as with a token alcf-tokens.sh wrote.
func LoginAge(tok Token, now time.Time) (time.Duration, bool) {
	if tok.AuthenticatedAt == 0 {
		return 0, false
	}
	return now.Sub(time.Unix(tok.AuthenticatedAt, 0)), true
}
