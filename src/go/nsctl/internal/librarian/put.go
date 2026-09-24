package librarian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hosturl"
	"io"
	"net/http"
	"time"
)

// LocalBaseURL is the control plane's own Artifact Librarian, reachable through
// hmd_proxy on loopback.
//
// The trailing slash is kept deliberately. Python's urljoin drops the last path
// segment when the base lacks one, so a slash-less base resolves to /apiop/...
// and misses nginx's location block entirely; Go's client trims the slash and
// concatenates, so the bug does not exist here. Keeping the slash and pinning it
// with a test is what stops a later refactor to url.JoinPath -- which resolves
// the same way urljoin does -- from reintroducing it.
func LocalBaseURL() string { return hosturl.Route("hmd_ms_artifact_lib") + "/" }

// LocalAPIKey is the literal the local librarian accepts. It is anonymous and
// reachable only on loopback; the threat model for a single-developer local
// platform does not include an attacker who already has that interface.
const LocalAPIKey = "local-dummy"

// maxPartSize is the Python client's max_part_size default. Every artifact in
// scope here is comfortably one part of it.
const maxPartSize = 100_000_000

// ErrMultipartRequired means the payload needs more parts than this client
// uploads, which is more than one.
//
// Refused rather than implemented, and loudly. The upstream client sends the
// content type only when there is exactly one part, so the multipart header
// contract is unobserved and cannot be ported faithfully -- and uploading the
// first part alone would store a corrupt zip that fails to unpack three commands
// later, with nothing in between to say why.
var ErrMultipartRequired = errors.New("the artifact needs a multipart upload, which this client does not do")

// NewLocal builds an anonymous client for the control plane's librarian.
//
// It deliberately does not go through New. New fails when neither an API key nor
// a token is configured, which is correct for a cloud librarian and wrong for
// this one: the local librarian wants no credential, and a `register` that
// demanded one would fail in exactly the environment nsctl exists to serve -- a
// machine whose only prerequisite is Docker.
//
// token is left empty rather than a flag being added for it: headers() already
// omits the Authorization header when there is none, so the zero value says
// "anonymous" without a second thing to keep in step.
func NewLocal(baseURL string) *Client {
	if baseURL == "" {
		baseURL = LocalBaseURL()
	}
	return &Client{
		BaseURL: trimSlash(baseURL),
		HTTP:    &http.Client{Timeout: 5 * time.Minute},
		apiKey:  LocalAPIKey,
	}
}

// filePart is one element of the put manifest's file_parts.
type filePart struct {
	PartNumber int `json:"part_number"`
	PartSize   int `json:"part_size"`
}

// uploadSpec is one presigned destination the service hands back.
type uploadSpec struct {
	UploadURL  string `json:"upload_url"`
	PartSize   int    `json:"part_size"`
	PartNumber int    `json:"part_number"`
	MimeType   string `json:"mime_type"`
}

type putResult struct {
	// Nid is conditional upstream -- put_file echoes it to close only `if "nid"
	// in put_result[0]` -- so it is omitempty here and echoed only when present.
	Nid         string       `json:"nid,omitempty"`
	Status      string       `json:"status,omitempty"`
	UploadSpecs []uploadSpec `json:"upload_specs"`
}

// partStatus is ByteUploader.get_status's shape, sent whole.
//
// The superset costs nothing and the alternative is guessing which fields the
// service reads; a close that silently means something else is not a failure
// that shows up anywhere near the upload.
type partStatus struct {
	PartNumber    int    `json:"part_number"`
	ETag          string `json:"etag"`
	BytesUploaded int    `json:"bytes_uploaded"`
	BytesTotal    int    `json:"bytes_total"`
	Tries         int    `json:"tries"`
	Skipped       bool   `json:"skipped"`
}

type closeResult struct {
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

// Put uploads an artifact to a content path, the three-leg exchange put_file
// performs: /apiop/put for presigned part URLs, a credential-free PUT per part,
// then /apiop/close with the ETags.
//
// Nothing is retried, and that is a decision rather than an omission. Both API
// legs are loopback and the upload is a single buffered part, so there is little
// for a retry to recover from -- and more to the point, a retry cannot be added
// later without also changing the body: replaying a request built over an
// io.Reader uploads zero bytes on the second attempt and reports success.
//
// SPEC010's boundary still holds: this writes to the *local* librarian.
// Publishing to a cloud one remains `hmd build` with HMD_AUTO_PUBLISH, and a
// second path into a shared store from a local-platform CLI would be a mistake
// regardless of how convenient it looked.
func (c *Client) Put(ctx context.Context, contentPath, contentItemType string, data []byte) error {
	if len(data) > maxPartSize {
		return fmt.Errorf("%s: %w: %d bytes, and the part size is %d",
			contentPath, ErrMultipartRequired, len(data), maxPartSize)
	}

	result, err := c.putManifest(ctx, contentPath, contentItemType, len(data))
	if err != nil {
		return err
	}
	if len(result.UploadSpecs) == 0 {
		return fmt.Errorf("put %s: the librarian returned no upload_specs", contentPath)
	}
	if len(result.UploadSpecs) > 1 {
		return fmt.Errorf("%s: %w: the librarian split %d bytes into %d parts",
			contentPath, ErrMultipartRequired, len(data), len(result.UploadSpecs))
	}

	spec := result.UploadSpecs[0]
	etag, err := c.uploadPart(ctx, contentPath, spec, data)
	if err != nil {
		return err
	}
	return c.closeUpload(ctx, contentPath, result.Nid, partStatus{
		PartNumber:    orOne(spec.PartNumber),
		ETag:          etag,
		BytesUploaded: len(data),
		BytesTotal:    len(data),
		Tries:         1,
	})
}

// putManifest is leg one. The body is a one-element *array*: the operation takes
// a list of manifests and answers with a list of results, and put_file sends one
// and reads [0].
func (c *Client) putManifest(ctx context.Context, contentPath, contentItemType string, size int) (putResult, error) {
	manifest := map[string]any{
		"content_item_path": contentPath,
		"file_parts":        []filePart{{PartNumber: 1, PartSize: size}},
	}
	if contentItemType != "" {
		manifest["content_item_type"] = contentItemType
	}

	var results []putResult
	if err := c.operation(ctx, "put", []any{manifest}, &results); err != nil {
		return putResult{}, err
	}
	if len(results) == 0 {
		return putResult{}, fmt.Errorf("put %s: the librarian returned no result", contentPath)
	}

	// Two statuses that are refusals rather than failures: the content path
	// names an entity that does not exist, or names one that cannot hold
	// content. Both arrive as HTTP 200.
	switch results[0].Status {
	case "invalid_path_missing_entity":
		return putResult{}, fmt.Errorf("put %s: the content path names an entity the librarian does not have", contentPath)
	case "invalid_path_entity":
		return putResult{}, fmt.Errorf("put %s: the content path is not a valid entity path", contentPath)
	}
	return results[0], nil
}

// uploadPart is leg two, and is the one with the Go-specific traps in it.
func (c *Client) uploadPart(ctx context.Context, contentPath string, spec uploadSpec, data []byte) (string, error) {
	if spec.UploadURL == "" {
		return "", fmt.Errorf("put %s: the upload spec carries no upload_url", contentPath)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, spec.UploadURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	// Set explicitly. Without it Go chooses a chunked transfer encoding for a
	// body it cannot size, and a presigned PUT rejects one -- the signature
	// covers a content length.
	req.ContentLength = int64(len(data))
	if spec.MimeType != "" {
		req.Header.Set("Content-Type", spec.MimeType)
	}
	// No credential on this leg, for the same reason Fetch sends none: the URL
	// is pre-signed, and a second set of credentials alongside a signature is
	// how an upload that works everywhere else 400s.

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("uploading %s: %w", contentPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", &Error{Operation: "uploading " + contentPath, Status: resp.StatusCode, Body: string(payload)}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	etag := resp.Header.Get("ETag")
	if etag == "" {
		// close names the part by its ETag, so an upload that produced none has
		// nothing to commit. Better here than as a close that reports success
		// over a content item holding no bytes.
		return "", fmt.Errorf("uploading %s: the upload returned no ETag", contentPath)
	}
	return etag, nil
}

// closeUpload is leg three, committing the parts the librarian now holds.
func (c *Client) closeUpload(ctx context.Context, contentPath, nid string, part partStatus) error {
	data := map[string]any{
		"content_item_path": contentPath,
		"upload_results":    []partStatus{part},
	}
	if nid != "" {
		data["nid"] = nid
	}

	var results []closeResult
	if err := c.operation(ctx, "close", []any{data}, &results); err != nil {
		return err
	}
	if len(results) == 0 {
		return fmt.Errorf("close %s: the librarian returned no result", contentPath)
	}
	if results[0].Status != "success" {
		// The message is where the service says what was wrong; the status alone
		// is "error".
		return fmt.Errorf("close %s: %s", contentPath, or(results[0].Message, results[0].Status))
	}
	return nil
}

// operation POSTs a custom operation, the way invoke_custom_operation does.
func (c *Client) operation(ctx context.Context, name string, payload any, out any) error {
	payloadBytes, err := c.request(ctx, http.MethodPost, "/apiop/"+name, payload, name)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payloadBytes, out); err != nil {
		return fmt.Errorf("%s: decoding the response: %w", name, err)
	}
	return nil
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func orOne(n int) int {
	if n == 0 {
		return 1
	}
	return n
}
