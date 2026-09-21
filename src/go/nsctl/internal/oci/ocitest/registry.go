// Package ocitest is an in-process OCI Distribution registry for tests.
//
// It serves the seven endpoints internal/oci speaks and nothing else, in
// memory, over httptest. It is a package rather than a _test.go file so that
// the consumers of internal/oci -- stacks, plugins, and the cmd tests that
// drive them -- can publish an artifact and pull it back without each writing
// its own fake. NERD016 Testing.
//
// The default behaviour is ghcr.io's for a public package: every /v2 request
// is challenged with a Bearer realm, and the realm hands out a token to an
// anonymous caller. RequireToken makes the realm demand HTTP Basic, which is a
// private package. NoChallenge is a plain registry with no auth at all.
package ocitest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// Option configures a Registry.
type Option func(*Registry)

// RequireToken makes the token realm demand HTTP Basic with these credentials.
// Without them the realm answers 401, which is what a private package does.
func RequireToken(user, secret string) Option {
	return func(r *Registry) { r.user, r.secret = user, secret }
}

// NoChallenge serves everything without authentication: no 401, no realm.
func NoChallenge() Option {
	return func(r *Registry) { r.noChallenge = true }
}

// PageSize caps tags/list pages so pagination is exercised.
func PageSize(n int) Option {
	return func(r *Registry) { r.pageSize = n }
}

// RejectArtifactType makes a manifest PUT carrying a top-level artifactType
// fail with 400, as a registry that predates OCI 1.1 does.
func RejectArtifactType() Option {
	return func(r *Registry) { r.rejectArtifactType = true }
}

// Registry is one fake.
type Registry struct {
	srv *httptest.Server

	user, secret       string
	noChallenge        bool
	pageSize           int
	rejectArtifactType bool

	mu        sync.Mutex
	manifests map[string]map[string]stored // name -> tag or digest -> manifest
	blobs     map[string]map[digest.Digest][]byte
	uploads   map[string]string // upload id -> name
	tokens    map[string]bool
	requests  []string
	nextTok   int
}

type stored struct {
	mediaType string
	body      []byte
}

// New starts a registry and stops it when the test ends.
func New(t testing.TB, opts ...Option) *Registry {
	t.Helper()
	r := &Registry{
		pageSize:  100,
		manifests: map[string]map[string]stored{},
		blobs:     map[string]map[digest.Digest][]byte{},
		uploads:   map[string]string{},
		tokens:    map[string]bool{},
	}
	for _, o := range opts {
		o(r)
	}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	return r
}

// URL is the registry's base URL (http://127.0.0.1:port).
func (r *Registry) URL() string { return r.srv.URL }

// Host is the host:port a Ref names this registry by.
func (r *Registry) Host() string { return strings.TrimPrefix(r.srv.URL, "http://") }

// Requests is every method and path served, in order.
func (r *Registry) Requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.requests...)
}

// Put stores a manifest under a tag, with its blobs, bypassing the upload
// protocol. The manifest is also addressable by its digest, which is
// returned.
func (r *Registry) Put(name, tag string, m v1.Manifest, blobs map[digest.Digest][]byte) digest.Digest {
	body, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	mt := m.MediaType
	if mt == "" {
		mt = v1.MediaTypeImageManifest
	}
	return r.PutRaw(name, tag, mt, body, blobs)
}

// PutRaw stores manifest bytes verbatim under a media type, for a test that
// wants to serve an index or a malformed manifest.
func (r *Registry) PutRaw(name, tag, mediaType string, body []byte, blobs map[digest.Digest][]byte) digest.Digest {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.manifests[name] == nil {
		r.manifests[name] = map[string]stored{}
	}
	if r.blobs[name] == nil {
		r.blobs[name] = map[digest.Digest][]byte{}
	}
	d := digest.FromBytes(body)
	s := stored{mediaType: mediaType, body: body}
	r.manifests[name][d.String()] = s
	if tag != "" {
		r.manifests[name][tag] = s
	}
	for bd, b := range blobs {
		r.blobs[name][bd] = b
	}
	return d
}

// Manifest returns what is stored under a tag or digest, for assertions.
func (r *Registry) Manifest(name, ref string) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.manifests[name][ref]
	return s.body, ok
}

// Blob returns a stored blob, for assertions.
func (r *Registry) Blob(name string, d digest.Digest) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.blobs[name][d]
	return b, ok
}

var (
	tagsRe      = regexp.MustCompile(`^/v2/(.+)/tags/list$`)
	manifestsRe = regexp.MustCompile(`^/v2/(.+)/manifests/([^/]+)$`)
	blobsRe     = regexp.MustCompile(`^/v2/(.+)/blobs/([^/]+)$`)
	uploadsRe   = regexp.MustCompile(`^/v2/(.+)/blobs/uploads/$`)
	uploadRe    = regexp.MustCompile(`^/v2/(.+)/blobs/uploads/([^/?]+)$`)
)

func (r *Registry) record(req *http.Request) {
	r.mu.Lock()
	r.requests = append(r.requests, req.Method+" "+req.URL.RequestURI())
	r.mu.Unlock()
}

func (r *Registry) serve(w http.ResponseWriter, req *http.Request) {
	r.record(req)
	path := req.URL.Path

	if path == "/token" {
		r.serveToken(w, req)
		return
	}
	if !strings.HasPrefix(path, "/v2/") && path != "/v2" {
		http.NotFound(w, req)
		return
	}
	if !r.noChallenge && !r.authorized(req) {
		scope := scopeFor(req)
		w.Header().Set("WWW-Authenticate",
			fmt.Sprintf(`Bearer realm="%s/token",service="ocitest",scope="%s"`, r.srv.URL, scope))
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`)
		return
	}
	if path == "/v2/" || path == "/v2" {
		w.WriteHeader(http.StatusOK)
		return
	}

	switch {
	case tagsRe.MatchString(path):
		r.serveTags(w, req, tagsRe.FindStringSubmatch(path)[1])
	case uploadsRe.MatchString(path) && req.Method == http.MethodPost:
		r.startUpload(w, req, uploadsRe.FindStringSubmatch(path)[1])
	case uploadRe.MatchString(path) && req.Method == http.MethodPut:
		m := uploadRe.FindStringSubmatch(path)
		r.finishUpload(w, req, m[1], m[2])
	case manifestsRe.MatchString(path):
		m := manifestsRe.FindStringSubmatch(path)
		r.serveManifest(w, req, m[1], m[2])
	case blobsRe.MatchString(path):
		m := blobsRe.FindStringSubmatch(path)
		r.serveBlob(w, req, m[1], m[2])
	default:
		http.NotFound(w, req)
	}
}

// scopeFor is the scope a registry names in its challenge: the repository in
// the path, and pull or pull,push by method.
func scopeFor(req *http.Request) string {
	name := ""
	for _, re := range []*regexp.Regexp{tagsRe, uploadsRe, uploadRe, manifestsRe, blobsRe} {
		if m := re.FindStringSubmatch(req.URL.Path); m != nil {
			name = m[1]
			break
		}
	}
	if name == "" {
		return "registry:catalog:*"
	}
	action := "pull"
	if req.Method == http.MethodPost || req.Method == http.MethodPut {
		action = "pull,push"
	}
	return "repository:" + name + ":" + action
}

func (r *Registry) authorized(req *http.Request) bool {
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tokens[strings.TrimPrefix(auth, "Bearer ")]
}

func (r *Registry) serveToken(w http.ResponseWriter, req *http.Request) {
	if r.user != "" {
		user, secret, ok := req.BasicAuth()
		if !ok || user != r.user || secret != r.secret {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"errors":[{"code":"UNAUTHORIZED","message":"bad credentials"}]}`)
			return
		}
	}
	r.mu.Lock()
	r.nextTok++
	tok := "tok-" + strconv.Itoa(r.nextTok)
	r.tokens[tok] = true
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": tok})
}

func (r *Registry) serveTags(w http.ResponseWriter, req *http.Request, name string) {
	r.mu.Lock()
	var tags []string
	for ref := range r.manifests[name] {
		if !strings.HasPrefix(ref, "sha256:") {
			tags = append(tags, ref)
		}
	}
	r.mu.Unlock()
	if tags == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"errors":[{"code":"NAME_UNKNOWN"}]}`)
		return
	}
	sort.Strings(tags)
	q := req.URL.Query()
	n := r.pageSize
	if v := q.Get("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed < n {
			n = parsed
		}
	}
	if last := q.Get("last"); last != "" {
		i := sort.SearchStrings(tags, last)
		if i < len(tags) && tags[i] == last {
			i++
		}
		tags = tags[i:]
	}
	page := tags
	if len(page) > n {
		page = page[:n]
		next := url.Values{"n": {strconv.Itoa(n)}, "last": {page[len(page)-1]}}
		w.Header().Set("Link", fmt.Sprintf(`</v2/%s/tags/list?%s>; rel="next"`, name, next.Encode()))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "tags": page})
}

func (r *Registry) serveManifest(w http.ResponseWriter, req *http.Request, name, ref string) {
	switch req.Method {
	case http.MethodGet, http.MethodHead:
		r.mu.Lock()
		s, ok := r.manifests[name][ref]
		r.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`)
			return
		}
		w.Header().Set("Content-Type", s.mediaType)
		w.Header().Set("Docker-Content-Digest", digest.FromBytes(s.body).String())
		w.Header().Set("Content-Length", strconv.Itoa(len(s.body)))
		w.WriteHeader(http.StatusOK)
		if req.Method == http.MethodGet {
			_, _ = w.Write(s.body)
		}
	case http.MethodPut:
		body, _ := io.ReadAll(req.Body)
		if r.rejectArtifactType && bytes.Contains(body, []byte(`"artifactType"`)) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"errors":[{"code":"MANIFEST_INVALID","message":"artifactType is not supported"}]}`)
			return
		}
		var m v1.Manifest
		if err := json.Unmarshal(body, &m); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		for _, d := range append([]v1.Descriptor{m.Config}, m.Layers...) {
			if _, ok := r.blobs[name][d.Digest]; !ok {
				r.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprintf(w, `{"errors":[{"code":"MANIFEST_BLOB_UNKNOWN","message":"%s"}]}`, d.Digest)
				return
			}
		}
		r.mu.Unlock()
		d := r.PutRaw(name, ref, req.Header.Get("Content-Type"), body, nil)
		w.Header().Set("Docker-Content-Digest", d.String())
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (r *Registry) serveBlob(w http.ResponseWriter, req *http.Request, name, ref string) {
	d, err := digest.Parse(ref)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	b, ok := r.blobs[name][d]
	r.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"errors":[{"code":"BLOB_UNKNOWN"}]}`)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Header().Set("Docker-Content-Digest", d.String())
	w.WriteHeader(http.StatusOK)
	if req.Method == http.MethodGet {
		_, _ = w.Write(b)
	}
}

func (r *Registry) startUpload(w http.ResponseWriter, _ *http.Request, name string) {
	r.mu.Lock()
	r.nextTok++
	id := "upload-" + strconv.Itoa(r.nextTok)
	r.uploads[id] = name
	r.mu.Unlock()
	// A relative Location, which is what ghcr.io returns and what a client
	// must resolve against the base URL rather than treat as absolute.
	w.Header().Set("Location", "/v2/"+name+"/blobs/uploads/"+id)
	w.WriteHeader(http.StatusAccepted)
}

func (r *Registry) finishUpload(w http.ResponseWriter, req *http.Request, name, id string) {
	r.mu.Lock()
	_, ok := r.uploads[id]
	r.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	want, err := digest.Parse(req.URL.Query().Get("digest"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	body, _ := io.ReadAll(req.Body)
	if got := digest.FromBytes(body); got != want {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"errors":[{"code":"DIGEST_INVALID"}]}`)
		return
	}
	r.mu.Lock()
	if r.blobs[name] == nil {
		r.blobs[name] = map[digest.Digest][]byte{}
	}
	r.blobs[name][want] = body
	delete(r.uploads, id)
	r.mu.Unlock()
	w.Header().Set("Docker-Content-Digest", want.String())
	w.WriteHeader(http.StatusCreated)
}

// BasicHeader is the Authorization value for a user and secret, for tests
// that assert on what was sent.
func BasicHeader(user, secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+secret))
}
