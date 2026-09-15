package librarian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
)

// Environment variables this package reads. They are the Python ones verbatim,
// because a developer who has a working `hmd build` must not have to configure
// anything a second time for `make generate`.
const (
	APIKeyEnv    = "HMD_ARTIFACT_LIBRARIAN_API_KEY"
	AuthTokenEnv = "HMD_AUTH_TOKEN"
)

// URLEnv, CustomerCodeEnv and RegionEnv are re-exported from nsconfig rather
// than spelled again here. They are read by the shared endpoint rule, not by
// this package, and two spellings of one variable name is the kind of drift
// that only shows up as a client talking to the wrong host.
const (
	URLEnv          = nsconfig.ArtifactLibrarianURLEnv
	CustomerCodeEnv = nsconfig.CustomerCodeEnv
	RegionEnv       = nsconfig.RegionEnv
)

// TokenRelPath is where `hmd login` caches its token, relative to $HMD_HOME --
// hmd_cache_folder_path() joined with okta_tools.HMD_TOKEN_FILENAME.
var TokenRelPath = filepath.Join(".cache", "tokens.yaml")

// ErrNoCredentials means nothing was configured to authenticate with. It is
// separated from a 401 deliberately: "you never set this up" and "your token
// expired" have different fixes and arrive as the same failed request
// otherwise.
var ErrNoCredentials = errors.New("no artifact librarian credentials")

// ErrNotPublished means the librarian answered, and has no such content item.
// This is the answer a version pin that was never published produces, and it
// is not an HTTP error -- the search simply returns an empty list.
var ErrNotPublished = errors.New("not published")

// Error carries the operation and the librarian's own response body, mirroring
// msdeploy.Error for the same reason: the body is where the service says what
// was actually wrong, and the status alone rarely is.
type Error struct {
	Operation string
	Status    int
	Body      string
}

func (e *Error) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 600 {
		body = body[:600] + "..."
	}
	msg := fmt.Sprintf("%s: HTTP %d: %s", e.Operation, e.Status, body)
	if e.Unauthorized() {
		msg += "\n  the credential was rejected; run `nsctl login` or `hmd login`, or set " + APIKeyEnv
	}
	return msg
}

// Unauthorized reports whether the credential was refused rather than the
// request being wrong. SPEC006 records `Unauthorized` as the reason seven repo
// classes went unpinned, so this is the case most worth naming.
func (e *Error) Unauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// Client is an artifact librarian connection.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	// Endpoint records how BaseURL was resolved, so a command can say which
	// service it is about to send a credential to and why. Zero for NewLocal,
	// whose address is a constant rather than a resolution.
	Endpoint nsconfig.Endpoint

	apiKey string
	token  string

	// The repo list, memoised: it is one unfiltered request that brings back
	// every repo at once, and every enumeration needs it.
	reposCache
}

// Config is what New needs. The zero value is usable: Lookup falls back to the
// process environment and Home to $HMD_HOME.
type Config struct {
	// Lookup resolves an environment variable. Defaults to a lookup that
	// answers from the process environment first and $HMD_HOME/.config/hmd.env
	// second, which is where HMD_CUSTOMER_CODE and HMD_REGION normally live.
	Lookup func(string) string
	// Home is $HMD_HOME, used to find the cached login token.
	Home string
	// Profile is the nsctl.toml profile whose endpoint keys apply. The zero
	// Profile contributes nothing, which is what "no profile named" means.
	Profile nsconfig.Profile
	// FlagURL is a --url the user typed, which beats everything else.
	FlagURL string
}

// New builds a client from the environment, resolving the endpoint and the
// credential the way get_artifact_librarian_client does.
//
// It fails when no credential is configured rather than at the first request,
// so a caller can report "this needs credentials" before it reports which of
// ten fetches failed.
func New(cfg Config) (*Client, error) {
	home := cfg.Home
	if home == "" {
		home = os.Getenv("HMD_HOME")
	}
	lookup := cfg.Lookup
	if lookup == nil {
		// A missing or unreadable hmd.env is not an error here: the process
		// environment alone is a complete configuration, and CI has no
		// HMD_HOME at all.
		file, _ := hmdenv.Load(home)
		lookup = hmdenv.Layered(os.Getenv, file)
	}

	base, err := nsconfig.ResolveEndpoint(nsconfig.ArtifactLibrarian, cfg.FlagURL, cfg.Profile, lookup)
	if err != nil {
		return nil, err
	}

	c := &Client{
		BaseURL:  trimSlash(base.URL),
		Endpoint: base,
		// Generous: these are multi-megabyte zips over a link that may be a
		// coffee-shop connection, and the whole fetch happens once per pin.
		HTTP:   &http.Client{Timeout: 5 * time.Minute},
		apiKey: lookup(APIKeyEnv),
		token:  authToken(lookup, home),
	}
	if c.apiKey == "" && c.token == "" {
		return nil, fmt.Errorf("%w: set %s, or run `nsctl login` to cache one in %s",
			ErrNoCredentials, APIKeyEnv, filepath.Join(homeOrPlaceholder(home), TokenRelPath))
	}
	return c, nil
}

// trimSlash normalises a base URL for the concatenation every request does.
//
// Shared with NewLocal rather than repeated: the local librarian's URL carries a
// trailing slash on purpose, and the one place that decides what happens to it
// should be the one place.
func trimSlash(base string) string { return strings.TrimRight(base, "/") }

// authToken is get_auth_token(): the environment first, then the file `hmd
// login` writes. A missing or malformed file yields no token rather than an
// error -- an API key alone is a complete credential, and this is the path CI
// takes.
func authToken(lookup func(string) string, home string) string {
	if token := lookup(AuthTokenEnv); token != "" {
		return token
	}
	if home == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, TokenRelPath))
	if err != nil {
		return ""
	}
	var cached struct {
		Login struct {
			AccessToken string `yaml:"access_token"`
		} `yaml:"login"`
	}
	if yaml.Unmarshal(data, &cached) != nil {
		return ""
	}
	return cached.Login.AccessToken
}

func homeOrPlaceholder(home string) string {
	if home == "" {
		return "$HMD_HOME"
	}
	return home
}

func (c *Client) headers(req *http.Request) {
	// Both, when both are present. _get_headers sends them together rather
	// than choosing, and a librarian that only honours one should be the one
	// deciding which.
	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
	}
	if c.token != "" {
		req.Header.Set("Authorization", c.token)
	}
}

// request performs one authenticated request and returns the body.
//
// The three shapes this client speaks -- POST /api/<entity>, GET
// /api/<relationship>/to/<id> and POST /apiop/<operation> -- differ only in
// method, path and body, so they share this rather than each carrying its own
// copy of the header and error handling.
func (c *Client) request(ctx context.Context, method, path string, payload any,
	operation string) ([]byte, error) {

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.headers(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	defer resp.Body.Close()
	payloadBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: reading the response: %w", operation, err)
	}
	if resp.StatusCode >= 400 {
		return nil, &Error{Operation: operation, Status: resp.StatusCode, Body: string(payloadBytes)}
	}
	return payloadBytes, nil
}

// DownloadURL resolves a content path to the URL its bytes can be read from.
//
// The librarian does not serve content inline: `get` searches for the content
// item and answers with a pre-signed download_url, which is then fetched
// unauthenticated. get_file does exactly this pair.
func (c *Client) DownloadURL(ctx context.Context, contentPath string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"attribute": "content_item_path",
		"operator":  "=",
		"value":     contentPath,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/apiop/get", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.headers(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("get %s: %w", contentPath, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("get %s: reading the response: %w", contentPath, err)
	}
	if resp.StatusCode >= 400 {
		return "", &Error{Operation: "get " + contentPath, Status: resp.StatusCode, Body: string(payload)}
	}

	var items []struct {
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(payload, &items); err != nil {
		return "", fmt.Errorf("get %s: decoding the response: %w", contentPath, err)
	}
	if len(items) == 0 {
		return "", fmt.Errorf("%s: %w", contentPath, ErrNotPublished)
	}
	if items[0].DownloadURL == "" {
		return "", fmt.Errorf("get %s: the content item carries no download_url", contentPath)
	}
	return items[0].DownloadURL, nil
}

// Fetch downloads the artifact at a content path. The result is a zip, which
// is what every content item type in the librarian is.
func (c *Client) Fetch(ctx context.Context, contentPath string) ([]byte, error) {
	url, err := c.DownloadURL(ctx, contentPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// No credential on this leg: the URL is pre-signed, and sending an
	// Authorization header to S3 alongside a signature is how a download that
	// works everywhere else 400s.
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", contentPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &Error{Operation: "downloading " + contentPath, Status: resp.StatusCode, Body: string(payload)}
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", contentPath, err)
	}
	return data, nil
}
