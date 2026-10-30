package alcfauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

// pkce is an RFC 7636 code verifier and its S256 challenge.
type pkce struct {
	Verifier  string
	Challenge string
}

// newPKCE draws a verifier from 32 random bytes, which base64url encodes to
// 43 characters, the shortest verifier RFC 7636 allows.
func newPKCE(random io.Reader) (pkce, error) {
	verifier, err := randomString(random)
	if err != nil {
		return pkce{}, err
	}
	return pkce{Verifier: verifier, Challenge: challengeS256(verifier)}, nil
}

func challengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomString(random io.Reader) (string, error) {
	if random == nil {
		random = rand.Reader
	}
	buf := make([]byte, 32)
	if _, err := io.ReadFull(random, buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
