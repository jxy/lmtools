// Package alcfauth logs in to the ALCF Inference Endpoints through Globus
// Auth, keeps the tokens in a local cache, and writes the access token to a
// key file that lmc and apiproxy read with -api-key-file.
//
// The login is the one `alcf-tokens login inference` performs: an OAuth2
// authorization code grant with PKCE for a public native client, where Globus
// shows the code and the user pastes it into the terminal. The identifiers
// below come from alcf-tokens 0.4.0 (auth.py and alcf-tokens.sh) and the
// ALCF user guide; recheck them there when ALCF changes its service.
package alcfauth

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// DefaultAuthURL is the Globus Auth service.
	DefaultAuthURL = "https://auth.globus.org"

	// RedirectURI is the Globus page that shows the authorization code for
	// the user to paste, so the login needs no local listener and works over
	// SSH.
	RedirectURI = "https://auth.globus.org/v2/web/auth-code"

	// DefaultClientID is the public native app alcf-tokens registered with
	// Globus. ALCF's own shell port of the login uses it as well.
	DefaultClientID = "7f3e61f5-e0de-4e8f-9150-0a62c65dda63"

	// DefaultGatewayURL is the root of the ALCF inference gateway. A cluster's
	// OpenAI compatible API sits below it, such as minerva/api/v1.
	DefaultGatewayURL = "https://inference-api.alcf.anl.gov/resource_server"

	// AppName goes in the token cache's app_name field.
	AppName = "lmtools"
)

// Service is a Globus resource server and the scope a login asks it for.
type Service struct {
	Name           string
	ResourceServer string
	Scope          string
	// SessionPolicy is sent as session_required_policies. For the inference
	// service it limits the login to the identity providers ALCF accepts.
	SessionPolicy string
}

// Inference is the ALCF Inference Service.
var Inference = Service{
	Name:           "inference",
	ResourceServer: "681c10cc-f684-4540-bcd7-0b4df3bc26ef",
	Scope:          "https://auth.globus.org/scopes/681c10cc-f684-4540-bcd7-0b4df3bc26ef/action_all",
	SessionPolicy:  "83732ff2-9c42-4548-b5ce-17e498c84f6a",
}

// DefaultDir is ~/.lmc/alcf, which holds the token cache and the key file.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".lmc", "alcf"), nil
}

// DefaultCachePath is ~/.lmc/alcf/tokens.json.
func DefaultCachePath() (string, error) {
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tokens.json"), nil
}

// DefaultKeyFilePath is ~/.lmc/alcf/inference.key.
func DefaultKeyFilePath() (string, error) {
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "inference.key"), nil
}
