package msdeploy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tokenstore"
)

// APIKeyEnv is the API Gateway key, when a tenant has turned API keys on.
//
// Read from the environment rather than fetched from AWS, which is what the
// Python does: hmd-cli-deploy calls apigateway:GetApiKeys under a named profile
// to find it. That needs AWS credentials, a region mapping and a boto client,
// which is a large dependency for a value that is normally absent --
// hmd-ms-deployment's own manifest sets `use_api_key: false`, and
// hmd-cli-deploy proceeds happily with no key at all. This variable is the
// escape hatch for a tenant that does use one.
const APIKeyEnv = "HMD_MS_DEPLOYMENT_API_KEY"

// AuthTokenEnv is the same variable internal/librarian and okta_tools read.
const AuthTokenEnv = "HMD_AUTH_TOKEN"

// ErrNoCredentials means nothing was configured to authenticate with.
//
// Separated from a 401 for the reason librarian.ErrNoCredentials is: "you never
// set this up" and "your token expired" have different fixes and arrive as the
// same failed request otherwise.
var ErrNoCredentials = errors.New("no deployment service credentials")

// CloudConfig is what NewCloud needs. The zero value is usable: Lookup falls
// back to the process environment layered over hmd.env, and Home to $HMD_HOME.
type CloudConfig struct {
	Lookup func(string) string
	Home   string
	// Profile is the nsctl.toml profile whose endpoint keys apply.
	Profile nsconfig.Profile
	// FlagURL is a --url the user typed, which beats everything else.
	FlagURL string
}

// NewCloud builds a client for a cloud hmd-ms-deployment.
//
// A second constructor on this package rather than a second package. The entity
// names, the Filter shape, the base64 collection encoding and the error type
// are one service's protocol, and the only differences between a control-plane
// client and a cloud one are an address and a credential. Splitting them would
// create two places for one fact and guarantee they drift.
func NewCloud(cfg CloudConfig) (*Client, error) {
	home := cfg.Home
	if home == "" {
		home = os.Getenv("HMD_HOME")
	}
	lookup := cfg.Lookup
	if lookup == nil {
		file, _ := hmdenv.Load(home)
		lookup = hmdenv.Layered(os.Getenv, file)
	}

	endpoint, err := nsconfig.ResolveEndpoint(nsconfig.Deployment, cfg.FlagURL, cfg.Profile, lookup)
	if err != nil {
		return nil, err
	}

	c := New(endpoint.URL)
	c.Endpoint = endpoint
	c.apiKey = lookup(APIKeyEnv)
	c.token = authToken(lookup, home)
	if c.apiKey == "" && c.token == "" {
		return nil, fmt.Errorf("%w: run `nsctl login`, or set %s",
			ErrNoCredentials, AuthTokenEnv)
	}
	// A cloud round trip over somebody's home connection, not a loopback call
	// to a container on this machine. The default 90 s is sized for the latter.
	c.HTTP.Timeout = 3 * time.Minute
	return c, nil
}

// authToken is get_auth_token(): the environment first, then the file both
// `nsctl login` and `hmd login` write.
//
// A missing or malformed file yields no token rather than an error -- an API
// key alone is a complete credential, and that is the path CI takes.
func authToken(lookup func(string) string, home string) string {
	if token := lookup(AuthTokenEnv); token != "" {
		return token
	}
	if home == "" {
		return ""
	}
	file, err := tokenstore.Load(home)
	if err != nil {
		return ""
	}
	return file.Login.AccessToken
}

// TokenPath names where a credential would be cached, for an error that has to
// tell someone where to look.
func TokenPath(home string) string {
	if home == "" {
		return filepath.Join("$HMD_HOME", tokenstore.RelPath)
	}
	return tokenstore.Path(home)
}

// headers authenticates a request, and does nothing for a local client.
//
// Both credentials when both are present, which is what hmd_rest_client's
// _get_headers does -- a service that honours only one should be the one
// deciding which.
//
// The Authorization value is the **raw token with no Bearer prefix**. That is
// not an oversight being copied: it is what every Python client sends and what
// the service's authorizer reads, and prefixing it would fail authentication
// against a service that works everywhere else.
func (c *Client) headers(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
	}
	if c.token != "" {
		req.Header.Set("Authorization", c.token)
	}
}

// APIOpGet calls a named operation over GET, returning the undecoded body.
//
// hmd-ms-base declares each operation's methods, and several of the read-only
// ones -- get_deployment_bom among them -- are GET with their arguments in the
// path. APIOp and APIOpRaw are POST-only, and a GET operation called with POST
// answers 405.
//
// Raw, because these operations answer with arrays as readily as objects, and
// APIOp discards a non-object body into an empty map rather than failing. A
// decoder that silently returns nothing is worse than one that errors.
func (c *Client) APIOpGet(ctx context.Context, operation string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, c.BaseURL+"/apiop/"+operation, nil, operation)
}
