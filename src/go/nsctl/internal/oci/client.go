package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versionspec"
)

// maxManifest caps a manifest or config blob read into memory. A stack's lock
// and a plugin's descriptor are kilobytes; anything near this is not one.
const maxManifest = 4 << 20

// Fetcher is the read side of a source. *Client implements it; SPEC008's
// deferred GitHub Releases source would too.
type Fetcher interface {
	// Tags lists the version-shaped tags of a repository, newest first.
	Tags(ctx context.Context, ref Ref) ([]string, error)
	// Fetch retrieves and verifies a manifest and its config blob.
	Fetch(ctx context.Context, ref Ref) (*Bundle, error)
	// Blob streams one verified blob to w.
	Blob(ctx context.Context, ref Ref, desc v1.Descriptor, w io.Writer) error
}

// Bundle is a fetched manifest with its config bytes in hand and its layers
// as descriptors a consumer fetches with Blob.
type Bundle struct {
	Ref             Ref
	Digest          digest.Digest
	ArtifactType    string
	Config          []byte
	ConfigMediaType string
	Layers          []v1.Descriptor
	Annotations     map[string]string
	Manifest        v1.Manifest
}

// Client is one registry connection.
type Client struct {
	HTTP       *http.Client
	Credential Credential
	UserAgent  string

	mu     sync.Mutex
	tokens map[string]string // challenge scope -> bearer token
	bound  string            // host a non-anonymous credential is bound to
}

// New builds a client presenting cred.
func New(cred Credential) *Client {
	return &Client{
		HTTP:       &http.Client{Timeout: 10 * time.Minute},
		Credential: cred,
		UserAgent:  "nsctl",
		tokens:     map[string]string{},
		bound:      cred.Host,
	}
}

// base is the registry's URL for a host: plain HTTP for a loopback address,
// where a test fake or a local registry lives, HTTPS for everything else.
func base(host string) string {
	h := host
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return "http://" + host
	}
	return "https://" + host
}

// bind enforces SPEC006's one-host rule for a non-anonymous credential: the
// first host used binds the client, and any other is refused before a
// request is made.
func (c *Client) bind(ref Ref) error {
	if c.Credential.Anonymous() {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bound == "" {
		c.bound = ref.Host
		return nil
	}
	if !strings.EqualFold(c.bound, ref.Host) {
		return fmt.Errorf("%w: the credential from %s was resolved for %s and will not be sent to %s",
			ErrBoundToHost, c.Credential.Source, c.bound, ref.Host)
	}
	return nil
}

// do performs one request with the credential, following a single
// WWW-Authenticate challenge. build is called for each attempt so a body can
// be re-sent.
func (c *Client) do(ctx context.Context, ref Ref, build func() (*http.Request, error)) (*http.Response, error) {
	if err := c.bind(ref); err != nil {
		return nil, err
	}
	attempt := func(auth string) (*http.Response, error) {
		req, err := build()
		if err != nil {
			return nil, err
		}
		req = req.WithContext(ctx)
		req.Header.Set("User-Agent", c.UserAgent)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		return c.HTTP.Do(req)
	}

	// First attempt: a cached token for any scope on this host beats a static
	// bearer, which beats nothing. A username/secret pair is never sent
	// until a challenge says where.
	resp, err := attempt(c.initialAuth())
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	scheme, params := parseChallenge(challenge)
	drain(resp)

	var auth string
	switch scheme {
	case "Bearer":
		token, err := c.token(ctx, ref, params)
		if err != nil {
			return nil, err
		}
		auth = "Bearer " + token
	case "Basic":
		if c.Credential.Anonymous() {
			return nil, c.refused(ref, "authorize", resp.StatusCode, "")
		}
		auth = basic(c.basicPair())
	default:
		return nil, c.refused(ref, "authorize", resp.StatusCode, "")
	}
	return attempt(auth)
}

func (c *Client) initialAuth() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, tok := range c.tokens {
		return "Bearer " + tok
	}
	if c.Credential.Bearer != "" {
		return "Bearer " + c.Credential.Bearer
	}
	return ""
}

// basicPair is what to present at a Basic prompt: the username and secret,
// or the bearer as the password with the default username.
func (c *Client) basicPair() (string, string) {
	if c.Credential.Secret != "" {
		return c.Credential.Username, c.Credential.Secret
	}
	user := c.Credential.Username
	if user == "" {
		user = DefaultUser
	}
	return user, c.Credential.Bearer
}

func basic(user, secret string) string {
	return "Basic " + base64Encode(user+":"+secret)
}

// token exchanges the credential at the challenge's realm. SPEC002.
func (c *Client) token(ctx context.Context, ref Ref, params map[string]string) (string, error) {
	realm := params["realm"]
	scope := params["scope"]
	if realm == "" {
		return "", c.refused(ref, "authorize", http.StatusUnauthorized, "challenge names no realm")
	}
	c.mu.Lock()
	if tok, ok := c.tokens[scope]; ok {
		c.mu.Unlock()
		return tok, nil
	}
	c.mu.Unlock()

	u, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("parsing the token realm %q: %w", realm, err)
	}
	q := u.Query()
	if service := params["service"]; service != "" {
		q.Set("service", service)
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if !c.Credential.Anonymous() {
		req.Header.Set("Authorization", basic(c.basicPair()))
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxManifest))
	if resp.StatusCode != http.StatusOK {
		return "", c.refused(ref, "authorize", resp.StatusCode, string(body))
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("token realm %s answered something other than a token: %w", realm, err)
	}
	tok := payload.Token
	if tok == "" {
		tok = payload.AccessToken
	}
	if tok == "" {
		return "", c.refused(ref, "authorize", resp.StatusCode, "token realm answered without a token")
	}
	c.mu.Lock()
	c.tokens[scope] = tok
	c.mu.Unlock()
	return tok, nil
}

func (c *Client) refused(ref Ref, op string, status int, body string) error {
	return &Error{Operation: op, Status: status, Body: body, Ref: ref, Source: c.Credential.Source}
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

func (c *Client) failure(ref Ref, op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return c.refused(ref, op, resp.StatusCode, string(body))
}

// AllTags lists every tag of a repository, unfiltered, in registry order.
func (c *Client) AllTags(ctx context.Context, ref Ref) ([]string, error) {
	next := base(ref.Host) + "/v2/" + ref.Repository + "/tags/list?n=100"
	var tags []string
	for next != "" {
		target := next
		resp, err := c.do(ctx, ref, func() (*http.Request, error) {
			return http.NewRequest(http.MethodGet, target, nil)
		})
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			return nil, c.failure(ref, "list tags", resp)
		}
		var page struct {
			Tags []string `json:"tags"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, maxManifest)).Decode(&page)
		link := resp.Header.Get("Link")
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("list tags %s: %w", ref, err)
		}
		tags = append(tags, page.Tags...)
		next = nextLink(link, target)
	}
	return tags, nil
}

// Tags is SPEC004: AllTags filtered to versions, newest first.
func (c *Client) Tags(ctx context.Context, ref Ref) ([]string, error) {
	all, err := c.AllTags(ctx, ref)
	if err != nil {
		return nil, err
	}
	var versions []string
	for _, t := range all {
		if versionspec.IsVersion(t) {
			versions = append(versions, t)
		}
	}
	versionspec.Sort(versions)
	return versions, nil
}

// nextLink resolves the rel="next" target of a Link header against the
// request URL, or returns "".
func nextLink(header, current string) string {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) && !strings.Contains(part, "rel=next") {
			continue
		}
		start, end := strings.Index(part, "<"), strings.Index(part, ">")
		if start < 0 || end <= start {
			continue
		}
		cur, err := url.Parse(current)
		if err != nil {
			return ""
		}
		next, err := cur.Parse(part[start+1 : end])
		if err != nil {
			return ""
		}
		return next.String()
	}
	return ""
}

// Manifest fetches and verifies one manifest. SPEC003.
func (c *Client) Manifest(ctx context.Context, ref Ref) (v1.Manifest, digest.Digest, error) {
	if !ref.Versioned() {
		return v1.Manifest{}, "", fmt.Errorf("%s names no tag or digest; resolve a version first", ref)
	}
	target := base(ref.Host) + "/v2/" + ref.Repository + "/manifests/" + ref.Reference()
	resp, err := c.do(ctx, ref, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", v1.MediaTypeImageManifest+", "+v1.MediaTypeImageIndex)
		return req, nil
	})
	if err != nil {
		return v1.Manifest{}, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return v1.Manifest{}, "", c.failure(ref, "fetch manifest", resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifest+1))
	if err != nil {
		return v1.Manifest{}, "", fmt.Errorf("fetch manifest %s: %w", ref, err)
	}
	if len(body) > maxManifest {
		return v1.Manifest{}, "", fmt.Errorf("fetch manifest %s: larger than %d bytes, which no artifact manifest is", ref, maxManifest)
	}

	got := digest.FromBytes(body)
	if hdr := resp.Header.Get("Docker-Content-Digest"); hdr != "" && hdr != got.String() {
		return v1.Manifest{}, "", &MismatchError{What: "manifest", Expected: digest.Digest(hdr), Actual: got}
	}
	if ref.Digest != "" && ref.Digest != got.String() {
		return v1.Manifest{}, "", &MismatchError{What: "manifest", Expected: digest.Digest(ref.Digest), Actual: got}
	}

	ct := resp.Header.Get("Content-Type")
	var probe struct {
		MediaType string `json:"mediaType"`
	}
	_ = json.Unmarshal(body, &probe)
	if strings.HasPrefix(ct, v1.MediaTypeImageIndex) || probe.MediaType == v1.MediaTypeImageIndex ||
		strings.Contains(ct, "manifest.list") {
		return v1.Manifest{}, "", fmt.Errorf("%s is an image index, not an artifact manifest; "+
			"a stack or plugin is published as one manifest with its platforms as layers (NERD016 SPEC003)", ref)
	}
	var m v1.Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return v1.Manifest{}, "", fmt.Errorf("fetch manifest %s: not an OCI manifest: %w", ref, err)
	}
	return m, got, nil
}

// Fetch is Manifest plus the verified config blob.
func (c *Client) Fetch(ctx context.Context, ref Ref) (*Bundle, error) {
	m, d, err := c.Manifest(ctx, ref)
	if err != nil {
		return nil, err
	}
	var config bytes.Buffer
	if m.Config.Digest != "" {
		if m.Config.Size > maxManifest {
			return nil, fmt.Errorf("%s: config blob is %d bytes, which no artifact config is", ref, m.Config.Size)
		}
		if err := c.Blob(ctx, ref, m.Config, &config); err != nil {
			return nil, err
		}
	}
	return &Bundle{
		Ref:             ref,
		Digest:          d,
		ArtifactType:    m.ArtifactType,
		Config:          config.Bytes(),
		ConfigMediaType: m.Config.MediaType,
		Layers:          m.Layers,
		Annotations:     m.Annotations,
		Manifest:        m,
	}, nil
}

// Blob streams a blob to w, verifying digest and size. Bytes reach w as they
// arrive, so a caller writing to disk must stage and rename; the error at the
// end is what says whether what it wrote is good. SPEC003.
func (c *Client) Blob(ctx context.Context, ref Ref, desc v1.Descriptor, w io.Writer) error {
	if err := desc.Digest.Validate(); err != nil {
		return fmt.Errorf("%s: descriptor digest %q: %w", ref, desc.Digest, err)
	}
	target := base(ref.Host) + "/v2/" + ref.Repository + "/blobs/" + desc.Digest.String()
	resp, err := c.do(ctx, ref, func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, target, nil)
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return c.failure(ref, "fetch blob", resp)
	}
	verifier := desc.Digest.Verifier()
	// One byte past the declared size is enough to know it is too long
	// without reading the rest of an oversized body.
	limited := io.LimitReader(resp.Body, desc.Size+1)
	n, err := io.Copy(io.MultiWriter(w, verifier), limited)
	if err != nil {
		return fmt.Errorf("fetch blob %s: %w", desc.Digest, err)
	}
	if n != desc.Size {
		return &MismatchError{What: "blob", Expected: desc.Digest, ExpectedSize: desc.Size, ActualSize: n}
	}
	if !verifier.Verified() {
		return &MismatchError{What: "blob", Expected: desc.Digest, Actual: "sha256:(does not match)"}
	}
	return nil
}

// HasBlob reports whether the registry already holds a blob under a
// repository.
func (c *Client) HasBlob(ctx context.Context, ref Ref, d digest.Digest) (bool, error) {
	target := base(ref.Host) + "/v2/" + ref.Repository + "/blobs/" + d.String()
	resp, err := c.do(ctx, ref, func() (*http.Request, error) {
		return http.NewRequest(http.MethodHead, target, nil)
	})
	if err != nil {
		return false, err
	}
	drain(resp)
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, c.refused(ref, "check blob", resp.StatusCode, "")
}

// PushBlob uploads a blob unless the registry has it. The body is buffered:
// the digest is needed before the first request and a challenge means
// sending it twice. SPEC005.
func (c *Client) PushBlob(ctx context.Context, ref Ref, mediaType string, r io.Reader, size int64) (v1.Descriptor, error) {
	if c.Credential.Anonymous() {
		return v1.Descriptor{}, ErrNoCredential
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return v1.Descriptor{}, err
	}
	if size >= 0 && int64(len(data)) != size {
		return v1.Descriptor{}, fmt.Errorf("push blob: read %d bytes, caller said %d", len(data), size)
	}
	d := digest.FromBytes(data)
	desc := v1.Descriptor{MediaType: mediaType, Digest: d, Size: int64(len(data))}

	present, err := c.HasBlob(ctx, ref, d)
	if err != nil {
		return v1.Descriptor{}, err
	}
	if present {
		return desc, nil
	}

	start := base(ref.Host) + "/v2/" + ref.Repository + "/blobs/uploads/"
	resp, err := c.do(ctx, ref, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, start, nil)
	})
	if err != nil {
		return v1.Descriptor{}, err
	}
	location := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusAccepted || location == "" {
		defer resp.Body.Close()
		return v1.Descriptor{}, c.failure(ref, "start upload", resp)
	}
	drain(resp)
	upload, err := url.Parse(start)
	if err != nil {
		return v1.Descriptor{}, err
	}
	upload, err = upload.Parse(location)
	if err != nil {
		return v1.Descriptor{}, fmt.Errorf("start upload: bad Location %q: %w", location, err)
	}
	q := upload.Query()
	q.Set("digest", d.String())
	upload.RawQuery = q.Encode()

	resp, err = c.do(ctx, ref, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodPut, upload.String(), bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.ContentLength = int64(len(data))
		req.Header.Set("Content-Type", "application/octet-stream")
		return req, nil
	})
	if err != nil {
		return v1.Descriptor{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return v1.Descriptor{}, c.failure(ref, "upload blob", resp)
	}
	return desc, nil
}

// PushManifest puts a manifest under the reference's tag and returns its
// digest. A registry that rejects the 1.1 artifactType field gets one retry
// without it. SPEC005.
func (c *Client) PushManifest(ctx context.Context, ref Ref, m v1.Manifest) (digest.Digest, error) {
	if c.Credential.Anonymous() {
		return "", ErrNoCredential
	}
	if ref.Tag == "" {
		return "", fmt.Errorf("push manifest: %s names no tag", ref)
	}
	if m.MediaType == "" {
		m.MediaType = v1.MediaTypeImageManifest
	}
	if m.SchemaVersion == 0 {
		m.Versioned = Versioned()
	}
	put := func(m v1.Manifest) (*http.Response, digest.Digest, error) {
		body, err := json.Marshal(m)
		if err != nil {
			return nil, "", err
		}
		target := base(ref.Host) + "/v2/" + ref.Repository + "/manifests/" + ref.Tag
		resp, err := c.do(ctx, ref, func() (*http.Request, error) {
			req, err := http.NewRequest(http.MethodPut, target, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			req.ContentLength = int64(len(body))
			req.Header.Set("Content-Type", m.MediaType)
			return req, nil
		})
		return resp, digest.FromBytes(body), err
	}

	resp, d, err := put(m)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusBadRequest && m.ArtifactType != "" {
		drain(resp)
		retry := m
		retry.ArtifactType = ""
		resp, d, err = put(retry)
		if err != nil {
			return "", err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", c.failure(ref, "push manifest", resp)
	}
	if hdr := resp.Header.Get("Docker-Content-Digest"); hdr != "" && hdr != d.String() {
		return "", &MismatchError{What: "manifest", Expected: d, Actual: digest.Digest(hdr)}
	}
	return d, nil
}

// SortedVersions is a convenience for a consumer holding tags from elsewhere.
func SortedVersions(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if versionspec.IsVersion(t) {
			out = append(out, t)
		}
	}
	versionspec.Sort(out)
	return out
}
