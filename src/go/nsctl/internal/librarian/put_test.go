package librarian

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// putServer serves all three legs of the exchange and records what arrived. The
// zero value serves a well-behaved librarian; the fields override one leg at a
// time.
type putServer struct {
	t *testing.T

	// Overrides.
	putStatus  int
	putBody    string
	uploadCode int
	noETag     bool
	closeBody  string
	partCount  int
	omitNid    bool
	omitMime   bool

	// Recorded.
	putRequest    []map[string]any
	closeRequest  []map[string]any
	uploadHeaders http.Header
	uploadLength  int64
	uploadBody    string
}

func (s *putServer) start() *httptest.Server {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.uploadHeaders = r.Header.Clone()
		s.uploadLength = r.ContentLength
		s.uploadBody = string(body)
		if s.uploadCode >= 400 {
			w.WriteHeader(s.uploadCode)
			_, _ = w.Write([]byte("AccessDenied"))
			return
		}
		if !s.noETag {
			w.Header().Set("ETag", `"the-etag"`)
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/apiop/put", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&s.putRequest)
		if s.putStatus >= 400 {
			w.WriteHeader(s.putStatus)
			_, _ = w.Write([]byte("the librarian fell over"))
			return
		}
		if s.putBody != "" {
			_, _ = w.Write([]byte(s.putBody))
			return
		}
		specs := []map[string]any{}
		for i := 1; i <= max(1, s.partCount); i++ {
			spec := map[string]any{
				"upload_url":  srv.URL + "/upload",
				"part_size":   10,
				"part_number": i,
			}
			if !s.omitMime {
				spec["mime_type"] = "application/zip"
			}
			specs = append(specs, spec)
		}
		result := map[string]any{"upload_specs": specs}
		if !s.omitNid {
			result["nid"] = "the-nid"
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{result})
	})

	mux.HandleFunc("/apiop/close", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&s.closeRequest)
		if s.closeBody != "" {
			_, _ = w.Write([]byte(s.closeBody))
			return
		}
		_, _ = w.Write([]byte(`[{"status": "success"}]`))
	})

	s.t.Cleanup(srv.Close)
	return srv
}

// TestPutPerformsTheThreeLegExchange pins the whole protocol against put_file,
// including the two Go-specific requirements on the presigned leg that nothing
// else would catch: a body Go would otherwise send chunked, and credentials it
// would otherwise be natural to attach.
func TestPutPerformsTheThreeLegExchange(t *testing.T) {
	t.Parallel()

	server := &putServer{t: t}
	srv := server.start()
	c := NewLocal(srv.URL)

	const path = "repository:/hmd-inf-x/0.1.4/hmd-inf-x_0.1.4_build.zip"
	payload := []byte("a zip, as far")
	if err := c.Put(context.Background(), path, "build", payload); err != nil {
		t.Fatal(err)
	}

	// Leg one: a one-element array, because the operation takes a list of
	// manifests and put_file sends one.
	if len(server.putRequest) != 1 {
		t.Fatalf("the put body held %d manifests, want exactly one", len(server.putRequest))
	}
	manifest := server.putRequest[0]
	if manifest["content_item_path"] != path {
		t.Errorf("content_item_path = %v", manifest["content_item_path"])
	}
	if manifest["content_item_type"] != "build" {
		t.Errorf("content_item_type = %v", manifest["content_item_type"])
	}
	parts, _ := manifest["file_parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("file_parts = %v, want exactly one part", manifest["file_parts"])
	}
	part, _ := parts[0].(map[string]any)
	if part["part_number"] != float64(1) || part["part_size"] != float64(len(payload)) {
		t.Errorf("file_parts[0] = %v, want part_number 1 and part_size %d", part, len(payload))
	}

	// Leg two.
	if server.uploadBody != string(payload) {
		t.Errorf("the upload body was %q", server.uploadBody)
	}
	// Without an explicit ContentLength Go sends a chunked body, which a
	// presigned PUT rejects: the signature covers a content length.
	if server.uploadLength != int64(len(payload)) {
		t.Errorf("Content-Length = %d, want %d; a chunked body is rejected by a presigned PUT",
			server.uploadLength, len(payload))
	}
	if got := server.uploadHeaders.Get("Transfer-Encoding"); got != "" {
		t.Errorf("Transfer-Encoding = %q, want none", got)
	}
	if got := server.uploadHeaders.Get("Content-Type"); got != "application/zip" {
		t.Errorf("Content-Type = %q, want the spec's mime_type", got)
	}
	// The URL is pre-signed. A credential alongside a signature is how an
	// upload that works everywhere else 400s -- the same reason Fetch sends
	// none.
	if got := server.uploadHeaders.Get("Authorization"); got != "" {
		t.Errorf("the presigned upload carried Authorization=%q, want none", got)
	}
	if got := server.uploadHeaders.Get("x-api-key"); got != "" {
		t.Errorf("the presigned upload carried x-api-key=%q, want none", got)
	}

	// Leg three: the ETag committed, and the nid echoed because the put carried
	// one.
	if len(server.closeRequest) != 1 {
		t.Fatalf("the close body held %d entries, want exactly one", len(server.closeRequest))
	}
	closed := server.closeRequest[0]
	if closed["content_item_path"] != path {
		t.Errorf("close content_item_path = %v", closed["content_item_path"])
	}
	if closed["nid"] != "the-nid" {
		t.Errorf("close nid = %v, want the one the put returned", closed["nid"])
	}
	results, _ := closed["upload_results"].([]any)
	if len(results) != 1 {
		t.Fatalf("upload_results = %v", closed["upload_results"])
	}
	result, _ := results[0].(map[string]any)
	if result["etag"] != `"the-etag"` {
		t.Errorf("etag = %v, want the one the upload returned", result["etag"])
	}
	// The full per-part status ByteUploader.get_status sends, not a pair.
	for _, field := range []string{"part_number", "etag", "bytes_uploaded", "bytes_total", "tries", "skipped"} {
		if _, ok := result[field]; !ok {
			t.Errorf("upload_results[0] has no %q; the whole get_status shape is sent", field)
		}
	}
	if result["bytes_uploaded"] != float64(len(payload)) || result["bytes_total"] != float64(len(payload)) {
		t.Errorf("upload_results[0] = %v, want both byte counts at %d", result, len(payload))
	}
}

// nid is conditional upstream -- put_file echoes it only `if "nid" in
// put_result[0]` -- so a close for a put that carried none must not invent one.
func TestPutOmitsAnAbsentNid(t *testing.T) {
	t.Parallel()

	server := &putServer{t: t, omitNid: true}
	srv := server.start()
	if err := NewLocal(srv.URL).Put(context.Background(), "repository:/x/0.1/x_0.1_build.zip", "build", []byte("z")); err != nil {
		t.Fatal(err)
	}
	if _, present := server.closeRequest[0]["nid"]; present {
		t.Errorf("close carried nid=%v for a put that returned none", server.closeRequest[0]["nid"])
	}
}

// TestPutRefusesMultipart. Uploading the first part alone would store a corrupt
// zip that fails to unpack three commands later, with nothing in between to say
// why -- so more than one part is a typed error naming the size, not a best
// effort.
func TestPutRefusesMultipart(t *testing.T) {
	t.Parallel()

	server := &putServer{t: t, partCount: 3}
	srv := server.start()
	err := NewLocal(srv.URL).Put(context.Background(), "repository:/x/0.1/x_0.1_build.zip", "build", []byte("zip"))
	if !errors.Is(err, ErrMultipartRequired) {
		t.Fatalf("Put() error = %v, want ErrMultipartRequired", err)
	}
	if !strings.Contains(err.Error(), "3 bytes") {
		t.Errorf("the error does not name the size: %v", err)
	}
	if server.closeRequest != nil {
		t.Error("a refused multipart upload still called close")
	}
}

func TestPutFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		server putServer
		want   string
	}{
		{
			name:   "the librarian fell over on put",
			server: putServer{putStatus: http.StatusInternalServerError},
			want:   "HTTP 500",
		},
		{
			name:   "the content path names no entity",
			server: putServer{putBody: `[{"status": "invalid_path_missing_entity"}]`},
			want:   "does not have",
		},
		{
			name:   "the content path is not an entity path",
			server: putServer{putBody: `[{"status": "invalid_path_entity"}]`},
			want:   "not a valid entity path",
		},
		{
			name:   "no upload specs at all",
			server: putServer{putBody: `[{"upload_specs": []}]`},
			want:   "no upload_specs",
		},
		{
			name:   "the presigned upload was refused",
			server: putServer{uploadCode: http.StatusForbidden},
			want:   "HTTP 403",
		},
		{
			// close names the part by its ETag, so an upload that produced none
			// has nothing to commit. Better here than as a close reporting
			// success over a content item holding no bytes.
			name:   "the upload returned no ETag",
			server: putServer{noETag: true},
			want:   "no ETag",
		},
		{
			name:   "close refused, and says why",
			server: putServer{closeBody: `[{"status": "error", "message": "checksum mismatch"}]`},
			want:   "checksum mismatch",
		},
		{
			name:   "close returned nothing",
			server: putServer{closeBody: `[]`},
			want:   "no result",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := tt.server
			server.t = t
			srv := server.start()
			err := NewLocal(srv.URL).Put(context.Background(),
				"repository:/hmd-inf-x/0.1.4/hmd-inf-x_0.1.4_build.zip", "build", []byte("a zip"))
			if err == nil {
				t.Fatal("Put() succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Put() error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// TestNewLocalNeedsNoCredential is the direct regression test for New's hard
// failure. New refuses when neither an API key nor a token is configured, which
// is correct for a cloud librarian and would make `register` impossible on the
// machine nsctl exists to serve: the local librarian is anonymous.
func TestNewLocalNeedsNoCredential(t *testing.T) {
	t.Parallel()

	// The environment New would read, empty -- no URL, no API key, no token, no
	// HMD_HOME to find a cached one in.
	if _, err := New(Config{Home: t.TempDir(), Lookup: lookupFrom(nil)}); err == nil {
		t.Fatal("New succeeded with no configuration at all; this test guards the wrong thing")
	}

	server := &putServer{t: t}
	srv := server.start()
	c := NewLocal(srv.URL)
	if err := c.Put(context.Background(), "repository:/x/0.1/x_0.1_build.zip", "build", []byte("a zip")); err != nil {
		t.Fatalf("NewLocal round trip failed: %v", err)
	}
	// Anonymous, but the literal key the local librarian expects still travels.
	if got := server.putRequest; got == nil {
		t.Fatal("the put never arrived")
	}
}

// The local librarian's key is a literal and its URL keeps a trailing slash.
//
// Python's urljoin drops the last path segment when the base lacks one, so a
// slash-less base resolves to /apiop/... and misses nginx's location block
// entirely. Go concatenates instead, so the bug does not exist here -- but
// url.JoinPath resolves the way urljoin does, and a refactor to it would bring
// the bug back.
func TestLocalBaseURLKeepsItsTrailingSlash(t *testing.T) {
	t.Parallel()

	if !strings.HasSuffix(LocalBaseURL(), "/") {
		t.Errorf("LocalBaseURL = %q, want a trailing slash", LocalBaseURL())
	}
	if LocalBaseURL() != "http://localhost/hmd_ms_artifact_lib/" {
		t.Errorf("LocalBaseURL = %q", LocalBaseURL())
	}
	if LocalAPIKey != "local-dummy" {
		t.Errorf("LocalAPIKey = %q, want the literal the local librarian accepts", LocalAPIKey)
	}
	// Whatever the constant carries, a request lands on the location block.
	c := NewLocal("")
	if got := c.BaseURL + "/apiop/put"; got != "http://localhost/hmd_ms_artifact_lib/apiop/put" {
		t.Errorf("the put URL is %q", got)
	}
}

// A payload larger than one part is refused before anything is contacted, so the
// caller learns the reason rather than watching a corrupt upload complete.
func TestPutRefusesAnOversizePayloadWithoutContactingAnything(t *testing.T) {
	t.Parallel()

	c := NewLocal("http://127.0.0.1:1/")
	err := c.Put(context.Background(), "repository:/x/0.1/x_0.1_build.zip", "build", make([]byte, maxPartSize+1))
	if !errors.Is(err, ErrMultipartRequired) {
		t.Fatalf("Put() error = %v, want ErrMultipartRequired", err)
	}
}
