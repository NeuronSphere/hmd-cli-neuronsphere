package librarian

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
)

func lookupFrom(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// TestNewResolvesThroughTheSharedRule is what is left of this package's own
// endpoint tests. The precedence rule moved to internal/nsconfig, because two
// services now share it; what stays librarian's business is that New actually
// goes through it rather than keeping a second copy.
func TestNewResolvesThroughTheSharedRule(t *testing.T) {
	t.Parallel()

	c, err := New(Config{
		Home:   t.TempDir(),
		Lookup: lookupFrom(map[string]string{CustomerCodeEnv: "hmdtr1", RegionEnv: "reg1", APIKeyEnv: "k"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://artifact-aaa-reg1.hmdtr1-admin-neuronsphere.io"; c.BaseURL != want {
		t.Errorf("BaseURL = %q, want %q", c.BaseURL, want)
	}
	if c.Endpoint.Source == "" {
		t.Error("Endpoint.Source is empty; a resolved address should say where it came from")
	}
}

// TestNewTakesTheProfilesURL proves the profile tier is reachable from here,
// which is the whole point of Config carrying one.
func TestNewTakesTheProfilesURL(t *testing.T) {
	t.Parallel()

	c, err := New(Config{
		Home:    t.TempDir(),
		Lookup:  lookupFrom(map[string]string{APIKeyEnv: "k"}),
		Profile: nsconfig.Profile{Name: "acme", ArtifactLibrarianURL: "https://librarian.example/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://librarian.example"; c.BaseURL != want {
		t.Errorf("BaseURL = %q, want %q", c.BaseURL, want)
	}
}

func TestAuthTokenPrecedence(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(home, TokenRelPath)
	if err := os.WriteFile(tokenFile, []byte("login:\n  access_token: from-the-file\n  id_token: ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := authToken(lookupFrom(map[string]string{AuthTokenEnv: "from-the-env"}), home); got != "from-the-env" {
		t.Errorf("authToken() = %q, want the environment to win", got)
	}
	if got := authToken(lookupFrom(nil), home); got != "from-the-file" {
		t.Errorf("authToken() = %q, want the cached login token", got)
	}
	if got := authToken(lookupFrom(nil), t.TempDir()); got != "" {
		t.Errorf("authToken() = %q, want empty when there is no token file", got)
	}
	// A malformed file is not an error: an API key alone is a complete
	// credential, and that is the path CI takes.
	bad := t.TempDir()
	_ = os.MkdirAll(filepath.Join(bad, ".cache"), 0o755)
	_ = os.WriteFile(filepath.Join(bad, TokenRelPath), []byte("\x00not yaml: [\n"), 0o600)
	if got := authToken(lookupFrom(nil), bad); got != "" {
		t.Errorf("authToken() = %q, want empty for an unparseable token file", got)
	}
}

func TestNewRefusesWithNoCredential(t *testing.T) {
	t.Parallel()

	_, err := New(Config{
		Home:   t.TempDir(),
		Lookup: lookupFrom(map[string]string{URLEnv: "https://librarian.example"}),
	})
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("New() error = %v, want ErrNoCredentials", err)
	}
}

func TestFetchSendsBothCredentialsAndFollowsTheDownloadURL(t *testing.T) {
	t.Parallel()

	var gotAPIKey, gotAuth, gotDownloadAuth string
	var gotBody map[string]any

	blob := []byte("a zip, as far as this test is concerned")
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotDownloadAuth = r.Header.Get("Authorization")
		_, _ = w.Write(blob)
	}))
	defer files.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apiop/get" {
			t.Errorf("path = %q, want /apiop/get", r.URL.Path)
		}
		gotAPIKey = r.Header.Get("x-api-key")
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode([]map[string]any{{"download_url": files.URL + "/blob"}})
	}))
	defer api.Close()

	c, err := New(Config{Lookup: lookupFrom(map[string]string{
		URLEnv:       api.URL,
		APIKeyEnv:    "the-key",
		AuthTokenEnv: "the-token",
	})})
	if err != nil {
		t.Fatal(err)
	}

	spec, _ := ParseSpec("hmd-vpc@0.2.41:build")
	data, err := c.Fetch(context.Background(), spec.ContentPath())
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != string(blob) {
		t.Errorf("Fetch() returned %q, want the download body", data)
	}
	if gotAPIKey != "the-key" || gotAuth != "the-token" {
		t.Errorf("headers: x-api-key=%q Authorization=%q, want both sent as _get_headers sends them", gotAPIKey, gotAuth)
	}
	// The download URL is pre-signed. Sending Authorization alongside an S3
	// signature is how a download that works everywhere else 400s.
	if gotDownloadAuth != "" {
		t.Errorf("the pre-signed download carried Authorization=%q, want none", gotDownloadAuth)
	}
	if gotBody["attribute"] != "content_item_path" || gotBody["operator"] != "=" ||
		gotBody["value"] != spec.ContentPath() {
		t.Errorf("request body = %v, want a content_item_path equality filter", gotBody)
	}
}

// TestFetchEmptyResultIsNotPublished separates "the librarian has no such
// version" from every other failure. It arrives as a 200 with an empty list,
// so without this it reads as success.
func TestFetchEmptyResultIsNotPublished(t *testing.T) {
	t.Parallel()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))
	defer api.Close()

	c, err := New(Config{Lookup: lookupFrom(map[string]string{URLEnv: api.URL, APIKeyEnv: "k"})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Fetch(context.Background(), "repository:/hmd-vpc/9.9.9/hmd-vpc_9.9.9_build.zip")
	if !errors.Is(err, ErrNotPublished) {
		t.Fatalf("Fetch() error = %v, want ErrNotPublished", err)
	}
}

func TestFetchUnauthorizedSaysSo(t *testing.T) {
	t.Parallel()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Unauthorized"))
	}))
	defer api.Close()

	c, err := New(Config{Lookup: lookupFrom(map[string]string{URLEnv: api.URL, APIKeyEnv: "stale"})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Fetch(context.Background(), "repository:/hmd-vpc/0.2.41/hmd-vpc_0.2.41_build.zip")

	var lerr *Error
	if !errors.As(err, &lerr) {
		t.Fatalf("Fetch() error = %v, want a *librarian.Error", err)
	}
	if !lerr.Unauthorized() {
		t.Errorf("Unauthorized() = false for HTTP %d", lerr.Status)
	}
	if !strings.Contains(lerr.Error(), "hmd login") {
		t.Errorf("the message does not say how to fix it: %s", lerr)
	}
}
