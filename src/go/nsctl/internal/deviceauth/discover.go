package deviceauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Metadata is the subset of an authorization server's discovery document this
// package needs.
//
// A subset rather than the whole document on purpose: everything else in there
// describes capabilities this client does not exercise, and decoding fields
// nobody reads turns a server adding one into a maintenance event here.
type Metadata struct {
	Issuer                      string   `json:"issuer"`
	DeviceAuthorizationEndpoint string   `json:"device_authorization_endpoint"`
	TokenEndpoint               string   `json:"token_endpoint"`
	GrantTypesSupported         []string `json:"grant_types_supported"`
}

// DiscoveryPaths are the well-known documents tried, in order.
//
// Both, because the ecosystem is split and neither name is universal. Okta
// publishes the OIDC document; Superset and Airflow build the OAuth one --
// `server_metadata_url` is OKTA_BASE_URL + "/.well-known/oauth-authorization-server"
// in both -- and authd.Server.Handler already serves the same document at both
// names for exactly this reason.
var DiscoveryPaths = []string{
	"/.well-known/openid-configuration",
	"/.well-known/oauth-authorization-server",
}

// Discover fetches the authorization server's metadata.
//
// issuer is the base every endpoint hangs off, not a bare hostname: an
// authorization server lives at a path (authd at /oauth2/{ns,services}, Okta
// at /oauth2/<id>), so appending the well-known path to a hostname finds
// nothing.
func Discover(ctx context.Context, client *http.Client, issuer string) (*Metadata, error) {
	base := strings.TrimRight(strings.TrimSpace(issuer), "/")
	if base == "" {
		return nil, fmt.Errorf("no authorization server URL")
	}

	var lastErr error
	for _, path := range DiscoveryPaths {
		meta, err := fetchMetadata(ctx, client, base+path)
		if err != nil {
			// Keep the first failure: it came from the document most servers
			// publish, so it is the more informative one to report if both
			// names fail.
			if lastErr == nil {
				lastErr = err
			}
			continue
		}
		if meta.TokenEndpoint == "" {
			lastErr = fmt.Errorf("%s names no token_endpoint", base+path)
			continue
		}
		if meta.DeviceAuthorizationEndpoint == "" {
			// Found and readable, but this server cannot do the grant. That is
			// a different problem from "no document here", and reporting it as
			// a missing field would send the user looking for a typo in a URL
			// that is correct.
			return nil, fmt.Errorf(
				"%s does not support the device authorization grant: its discovery document at %s "+
					"names no device_authorization_endpoint.\n"+
					"  On Okta this usually means the authorization server is right and the "+
					"application is not: the grant has to be enabled on a native application.",
				base, base+path)
		}
		return meta, nil
	}
	return nil, fmt.Errorf("no OAuth discovery document under %s: %w", base, lastErr)
}

func fetchMetadata(ctx context.Context, client *http.Client, url string) (*Metadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("fetching %s: reading the response: %w", url, err)
	}
	if resp.StatusCode >= 400 {
		return nil, &Error{Operation: "fetching " + url, Status: resp.StatusCode, Body: string(body)}
	}

	var meta Metadata
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("fetching %s: decoding the response: %w", url, err)
	}
	return &meta, nil
}
