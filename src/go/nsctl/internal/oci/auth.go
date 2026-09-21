package oci

import (
	"encoding/base64"
	"net/url"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tokenstore"
)

// Environment variables SPEC006 reads.
const (
	// TokenEnv is a registry token or personal access token. With a username
	// from UserEnv it is exchanged at the registry's realm with HTTP Basic,
	// which is what every registry that issues PATs expects.
	TokenEnv = "HMD_REGISTRY_TOKEN"
	// UserEnv is the Basic username to present with TokenEnv.
	UserEnv = "HMD_REGISTRY_USER"
	// DefaultUser is the username when UserEnv is unset. ghcr.io ignores it
	// for a PAT; a registry that checks it will have told the user what to
	// set.
	DefaultUser = "nsctl"
)

// Credential is what the client presents to a registry, and where it came
// from.
//
// Exactly one shape is in use at a time: nothing (anonymous), a Username and
// Secret to exchange at the realm, or a Bearer to send outright. A Bearer is
// also offered as the Basic password on a challenge, because a registry that
// fronts the platform's login validates the same JWT both ways.
type Credential struct {
	Username string
	Secret   string
	Bearer   string

	// Source is the tier of SPEC006 that produced this, for the fetch line
	// and for a rejection message. Never the credential itself.
	Source string

	// Host is the registry the credential was resolved for. A non-anonymous
	// credential is presented to that host and its token realm and to
	// nothing else; a client bound to one host refuses a reference to
	// another before making a request.
	Host string
}

// Anonymous reports whether there is nothing to present.
func (c Credential) Anonymous() bool {
	return c.Username == "" && c.Secret == "" && c.Bearer == ""
}

// ResolveCredential is SPEC006's precedence for host: the push verb's --token
// flag, then TokenEnv (with UserEnv), then the profile whose registry_url
// names the host with the login token from tokenstore, then nothing.
//
// lookup is the layered environment (process over hmd.env). home may be "";
// profiles may be nil. A missing or malformed token file yields no credential
// rather than an error, as librarian.authToken does: an anonymous pull is a
// complete request.
func ResolveCredential(host, flagToken string, lookup func(string) string, home string,
	profiles []nsconfig.Profile) Credential {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	if tok := strings.TrimSpace(flagToken); tok != "" {
		return Credential{Username: DefaultUser, Secret: tok, Source: "--token", Host: host}
	}
	if tok := strings.TrimSpace(lookup(TokenEnv)); tok != "" {
		user := strings.TrimSpace(lookup(UserEnv))
		if user == "" {
			user = DefaultUser
		}
		return Credential{Username: user, Secret: tok, Source: TokenEnv, Host: host}
	}
	for _, p := range profiles {
		if !registryMatches(p.RegistryURL, host) {
			continue
		}
		if token := loginToken(home); token != "" {
			return Credential{Bearer: token, Source: "profile " + p.Name, Host: host}
		}
	}
	return Credential{Source: "anonymous"}
}

// registryMatches reports whether a profile's registry_url names host.
func registryMatches(registryURL, host string) bool {
	registryURL = strings.TrimSpace(registryURL)
	if registryURL == "" {
		return false
	}
	if !strings.Contains(registryURL, "://") {
		registryURL = "https://" + registryURL
	}
	u, err := url.Parse(registryURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

// loginToken is the access token `nsctl login` cached, or "".
func loginToken(home string) string {
	if home == "" {
		return ""
	}
	file, err := tokenstore.Load(home)
	if err != nil || file == nil {
		return ""
	}
	return strings.TrimSpace(file.Login.AccessToken)
}

// parseChallenge reads a WWW-Authenticate value: the scheme, then
// key=value parameters where a value may be quoted and a quoted value may
// contain commas ("scope=\"repository:a/b:pull,push\"").
func parseChallenge(header string) (scheme string, params map[string]string) {
	params = map[string]string{}
	header = strings.TrimSpace(header)
	if header == "" {
		return "", params
	}
	scheme, rest, _ := strings.Cut(header, " ")
	switch strings.ToLower(scheme) {
	case "bearer":
		scheme = "Bearer"
	case "basic":
		scheme = "Basic"
	}
	rest = strings.TrimSpace(rest)
	for rest != "" {
		key, after, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		key = strings.ToLower(strings.TrimSpace(key))
		after = strings.TrimLeft(after, " ")
		var value string
		if strings.HasPrefix(after, `"`) {
			end := strings.Index(after[1:], `"`)
			if end < 0 {
				value, rest = after[1:], ""
			} else {
				value, rest = after[1:1+end], after[2+end:]
			}
		} else {
			value, rest, _ = strings.Cut(after, ",")
			value = strings.TrimSpace(value)
		}
		params[key] = value
		rest = strings.TrimLeft(strings.TrimSpace(rest), ",")
		rest = strings.TrimSpace(rest)
	}
	return scheme, params
}

func base64Encode(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
