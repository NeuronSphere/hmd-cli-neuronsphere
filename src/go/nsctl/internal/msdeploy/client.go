// Package msdeploy talks to hmd-ms-deployment.
//
// Nothing here is a library swap; it is protocol. The service is an hmd-ms-base
// microservice, so it exposes two shapes: a CRUD surface at /api/<entity> and
// named operations at /apiop/<operation>. Both have sharp edges this package
// exists to hide.
package msdeploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
)

// Client is an ms-deployment connection.
//
// One type for two very different services: the control plane's own, reached
// over loopback with no credential, and a cloud one reached over the internet
// with an OIDC token. New builds the first and NewCloud the second; everything
// below that line is the same protocol.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	// Endpoint records how BaseURL was resolved, for a cloud client. Zero for
	// New, whose address is configuration rather than a resolution.
	Endpoint nsconfig.Endpoint

	apiKey string
	token  string
}

// New builds a client for the control-plane route.
func New(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 90 * time.Second}}
}

// Error carries the operation and the service's own response, because the body
// is where ms-deployment says what was actually wrong -- "required role,
// rds-loggroup, not provided" is a 400 with a useful body and a useless status.
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
	return fmt.Sprintf("%s: HTTP %d: %s", e.Operation, e.Status, body)
}

// AlreadyExists reports whether the service refused because the thing is
// already there. add_repo_class_version answers 400 "RepoClass, X, already has
// version Y", and re-registering has to be a no-op: the same repo class is
// registered by more than one seeding path.
func (e *Error) AlreadyExists() bool {
	return e.Status == http.StatusBadRequest && strings.Contains(strings.ToLower(e.Body), "already")
}

// EncodeCollection encodes a collection or mapping attribute for the CRUD PUT
// endpoint.
//
// hmd_ms_base transmits these as a base64-encoded JSON string: the request
// model types the field as str, so a native list is rejected 422 "Input should
// be a valid string", and the deserializer base64-decodes it, so a plain JSON
// string fails base64 decoding. Both deployment_set.definition and
// change_set.definition are collection attributes.
func EncodeCollection(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encoding a collection attribute: %w", err)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// DecodeCollection reads a collection attribute back, however the CRUD layer
// served it: base64-encoded JSON, or a native list if that ever changes.
func DecodeCollection(value any) ([]map[string]any, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out, nil
	case string:
		if decoded, err := base64.StdEncoding.DecodeString(v); err == nil {
			var out []map[string]any
			if err := json.Unmarshal(decoded, &out); err == nil {
				return out, nil
			}
		}
		var out []map[string]any
		if err := json.Unmarshal([]byte(v), &out); err == nil {
			return out, nil
		}
		return nil, fmt.Errorf("could not decode a collection attribute")
	default:
		return nil, fmt.Errorf("unexpected collection attribute of type %T", value)
	}
}

func (c *Client) do(ctx context.Context, method, url string, body any, operation string) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("%s: encoding the request: %w", operation, err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.headers(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: reading the response: %w", operation, err)
	}
	if resp.StatusCode >= 400 {
		return payload, &Error{Operation: operation, Status: resp.StatusCode, Body: string(payload)}
	}
	return payload, nil
}

// PutEntity creates an entity through the CRUD endpoint.
//
// ms-base PUT always takes the create branch -- it never upserts on a business
// key -- so every caller that must not duplicate a row has to search first.
func (c *Client) PutEntity(ctx context.Context, entityType string, data map[string]any) (map[string]any, error) {
	payload, err := c.do(ctx, http.MethodPut, c.BaseURL+"/api/"+entityType, data, "PUT "+entityType)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("PUT %s: decoding the response: %w", entityType, err)
	}
	return out, nil
}

// Filter is one ms-base search clause.
type Filter struct {
	Attribute string `json:"attribute"`
	Operator  string `json:"operator"`
	Value     any    `json:"value"`
}

// MarshalJSON emits {} for the zero Filter, which is what "everything" means.
//
// The struct's own encoding is {"attribute": "", "operator": "", "value":
// null}, and ms-deployment answers that with a 500: it takes the empty string
// as an attribute name to filter on and fails building the SQL. Python's
// _search_entities passes a bare {} for an unfiltered search, so this is what
// the service expects rather than a nicety.
//
// Only a wholly empty filter is special-cased. A filter naming an attribute
// serialises in full, including a Value that is empty or false, which
// omitempty on the field would have silently dropped.
func (f Filter) MarshalJSON() ([]byte, error) {
	if f.Attribute == "" && f.Operator == "" && f.Value == nil {
		return []byte("{}"), nil
	}
	type filter Filter // shed the method, so this does not recurse
	return json.Marshal(filter(f))
}

// Search finds entities. The filter is the top-level body, not a wrapper.
func (c *Client) Search(ctx context.Context, entityType string, filter Filter) ([]map[string]any, error) {
	payload, err := c.do(ctx, http.MethodPost, c.BaseURL+"/api/"+entityType, filter, "search "+entityType)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("search %s: decoding the response: %w", entityType, err)
	}
	return out, nil
}

// APIOp calls a named operation. Pass a nil payload for operations that take
// none.
func (c *Client) APIOp(ctx context.Context, operation string, payload any) (map[string]any, error) {
	body, err := c.do(ctx, http.MethodPost, c.BaseURL+"/apiop/"+operation, payload, operation)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		// Some operations answer with a bare value or an array; the caller that
		// needs those uses APIOpRaw.
		return map[string]any{}, nil
	}
	return out, nil
}

// APIOpRaw calls a named operation and returns the undecoded body, for the
// operations that do not answer with an object.
func (c *Client) APIOpRaw(ctx context.Context, operation string, payload any) ([]byte, error) {
	return c.do(ctx, http.MethodPost, c.BaseURL+"/apiop/"+operation, payload, operation)
}

// APIOpTolerateExists calls an operation, treating "already exists" as success.
func (c *Client) APIOpTolerateExists(ctx context.Context, operation string, payload any) error {
	_, err := c.APIOp(ctx, operation, payload)
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.AlreadyExists() {
		return nil
	}
	return err
}

// reachableAttempts and reachableBackoff bound the retry a single Reachable
// call makes. Floci evicts an idle Lambda container on its own timer,
// independent of request timing, and cold-starts a fresh one in under a
// second on the next request -- a probe landing in that gap is a transient
// miss, not the service being down, so one failed attempt must not read as
// "not answering."
const (
	reachableAttempts = 3
	reachableBackoff  = 300 * time.Millisecond
)

// Reachable reports whether the service answers at all. Retries a few times
// with a short backoff before giving up -- see reachableAttempts.
func (c *Client) Reachable(ctx context.Context) bool {
	for attempt := 1; attempt <= reachableAttempts; attempt++ {
		if c.reachableOnce(ctx) {
			return true
		}
		if attempt == reachableAttempts {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(reachableBackoff):
		}
	}
	return false
}

func (c *Client) reachableOnce(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/", nil)
	if err != nil {
		return false
	}
	c.headers(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// hmd-ms-base registers only /api/... and /apiop/... routes, so a 404 from
	// the root still confirms the chain is wired.
	return resp.StatusCode < 500
}

// serviceVersionTimeout bounds the OpenAPI fetch. Short, and its own budget
// rather than the client's 90 s: the only caller is `nsctl version`, the
// command you run to check an install, which must answer even when the control
// plane is half up.
const serviceVersionTimeout = 2 * time.Second

// ServiceVersion is the version the service reports for itself, and whether it
// answered at all.
//
// It comes from the OpenAPI schema's info.version, which hmd-base-service
// builds from HMD_REPO_VERSION -- the same variable nsctl sets when it deploys
// the Lambda (floci.ServiceEnv). There is no dedicated version route to ask
// instead: hmd-ms-base registers only /api/... and /apiop/..., which is why
// Reachable above settles for a 404 from the root.
func (c *Client) ServiceVersion(ctx context.Context) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, serviceVersionTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/openapi.json", nil)
	if err != nil {
		return "", false
	}
	c.headers(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", false
	}
	var doc struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return "", false
	}
	return doc.Info.Version, doc.Info.Version != ""
}
