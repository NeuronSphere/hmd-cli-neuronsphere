// Package nsconfig reads $HMD_HOME/.config/nsctl.toml, nsctl's own
// configuration file: the tenants it can reach, one profile each.
//
// A profile names an identity provider's issuer and, optionally, the addresses
// of that tenant's services. It was called logincfg while signing in was the
// only thing it configured; the name was widened rather than a second file
// invented, because "the acme tenant" is one fact and splitting it across two
// files would be two places to keep a customer code in step.
//
// TOML rather than the YAML every manifest uses, and a new file rather than a
// key in hmd.env, because this is the one piece of configuration a brand new
// user has to supply before anything else works. hmd.env is a flat dotenv
// namespace shared with sixty other variables; an endpoint plus its scopes
// plus its audience, once per profile, is a nested structure that a dotenv
// file can only encode by inventing prefixes.
//
// Profiles are the shape rather than a single endpoint because the same
// person may hold accounts in more than one place: an existing customer with
// two admin accounts, or anyone working across tenants. One extra table level
// costs nothing and cannot be retrofitted without changing the file format
// under people who already wrote one.
package nsconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/atomicfile"
)

// Lookup resolves an environment variable, returning "" when unset.
type Lookup = func(string) string

// Name is the config file's base name.
const Name = "nsctl.toml"

// PathOverride names an explicit config path, bypassing $HMD_HOME entirely.
//
// The analogue of manifest.PathOverride and used for the same reasons: one-off
// runs against another endpoint, and tests that must not depend on $HMD_HOME's
// layout.
const PathOverride = "HMD_LOCAL_NSCTL_CONFIG"

// DefaultScopes is what a profile requests when it names none.
//
// openid/email/profile are what identify the user, `groups` is the claim every
// Rego policy and both application role mappings read, and offline_access is
// what makes a refresh token possible. The last one is the difference between
// signing in once a day and signing in every hour: the Okta CLI app the Python
// login uses is provisioned without it, which is why `hmd login` has no refresh
// path at all.
var DefaultScopes = []string{"openid", "email", "profile", "groups", "offline_access"}

// Config is the whole file.
type Config struct {
	// DefaultProfile is the profile used when none is named. Optional when
	// the file defines exactly one.
	DefaultProfile string `toml:"default_profile"`
	// Profiles are the [profile.<name>] tables.
	Profiles map[string]Profile `toml:"profile"`
}

// Profile is one endpoint's configuration.
type Profile struct {
	// Name is the table's key. Filled in by Profile(), never read from the
	// file, so a table cannot disagree with its own name.
	Name string `toml:"-"`

	// AuthURL is the OAuth issuer -- the base every endpoint hangs off and
	// the value discovery is appended to. Required.
	//
	// The issuer, not a host: nsctl asks for {AuthURL}/.well-known/..., and
	// an authorization server is reached at a path (authd serves /oauth2/ns
	// and /oauth2/services; Okta's org servers are /oauth2/<id>). A bare
	// hostname resolves to no discovery document at all.
	AuthURL string `toml:"auth_url"`

	// ClientID is the OAuth client.
	//
	// Optional here and required in practice against a real provider, which
	// registers its own applications: Okta and Auth0 both reject a device
	// authorization request naming a client they do not know. It stays optional
	// because the local mock registers no clients and accepts any id, and
	// because a client id is public -- the device grant is a public-client flow
	// with no secret, so this is configuration, never a credential.
	ClientID string `toml:"client_id"`

	// Audience is an optional `audience` parameter. Auth0 requires one to
	// issue a JWT access token rather than an opaque one; Okta derives it
	// from the authorization server and ignores this.
	Audience string `toml:"audience"`

	// Scopes requested. Defaults to DefaultScopes.
	Scopes []string `toml:"scopes"`

	// CustomerCode and Region identify the tenant, and are what both service
	// hostnames are composed from when neither is named outright. Naming these
	// two is the shortest complete description of a tenant there is; see
	// ResolveEndpoint.
	CustomerCode string `toml:"customer_code"`
	Region       string `toml:"region"`

	// DeploymentURL and ArtifactLibrarianURL name the tenant's services
	// outright, for a deployment that does not follow the composed naming --
	// and for the local mocks a test points at.
	//
	// Optional, and expected to stay unset for an ordinary tenant: two long
	// URLs that repeat the customer code twice are a worse way to say what
	// CustomerCode and Region already say.
	DeploymentURL        string `toml:"deployment_url"`
	ArtifactLibrarianURL string `toml:"artifact_librarian_url"`
}

// DefaultClientID is the client id used when a profile names none.
const DefaultClientID = "nsctl"

// BuiltinProfileName is the name the compiled-in endpoint appears under.
const BuiltinProfileName = "neuronsphere"

// DefaultAuthURL is the endpoint a build signs in against when nothing is
// configured. Injected at link time:
//
//	-X github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig.DefaultAuthURL=https://...
//
// **Empty by default, and that is deliberate.** A release build pointed at the
// hosted NeuronSphere tenant lets someone install the binary and run `nsctl
// login` having configured nothing, which is the whole point of a signup step;
// a development build has no such tenant to name, and a constant naming a host
// that does not answer would be worse than refusing. So the refusal stands
// until a build sets this.
//
// Defaulting to the vendor's own endpoint is not the thing the "never guess an
// endpoint" rule guards against. That rule exists because an endpoint is where
// credentials are sent, so nsctl must never *infer* one. A constant compiled
// into the binary is not an inference -- it is this build's own identity, the
// same one `gh auth login`, `stripe login` and `vercel login` all carry.
var DefaultAuthURL = ""

// Builtin is the compiled-in profile, if this build has one.
func Builtin() (Profile, bool) {
	url := strings.TrimSpace(DefaultAuthURL)
	if url == "" {
		return Profile{}, false
	}
	return Profile{Name: BuiltinProfileName, AuthURL: url}.withDefaults(), true
}

// Resolve finds the profile to sign in with, falling back to the compiled-in
// endpoint when the file does not answer.
//
// The file always wins: a build's default is what to do when nobody has said
// otherwise, never an override of someone who has. So a [profile.neuronsphere]
// table replaces the built-in entirely rather than merging with it -- a partial
// override would mean the endpoint someone reads in their own file is not the
// one that gets used.
//
// The built-in only answers for its own name or for no name at all. A caller
// that asked for "staging" and has no file wants to hear that "staging" is
// undefined, not to be signed in somewhere else.
func Resolve(cfg *Config, name string) (Profile, error) {
	if cfg != nil && len(cfg.Profiles) > 0 {
		profile, err := cfg.Profile(name)
		if err == nil {
			return profile, nil
		}
		if builtin, ok := Builtin(); ok && (name == "" || name == BuiltinProfileName) {
			return builtin, nil
		}
		return Profile{}, err
	}
	if builtin, ok := Builtin(); ok && (name == "" || name == BuiltinProfileName) {
		return builtin, nil
	}
	return Profile{}, ErrNoProfiles
}

// ErrNoConfig means there is no config file. It is separated from a parse
// failure deliberately: "you have not set this up" is answered by prompting,
// and "your file is wrong" must never be answered by silently overwriting it.
var ErrNoConfig = errors.New("no nsctl config")

// ErrNoProfiles means the file parsed but defines no [profile.<name>] table.
var ErrNoProfiles = errors.New("no profiles defined")

// Path is where the config lives.
func Path(home string, lookup Lookup) string {
	if lookup != nil {
		if override := strings.TrimSpace(lookup(PathOverride)); override != "" {
			return os.Expand(override, lookup)
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config", Name)
}

// Load reads the config.
//
// A missing file is ErrNoConfig rather than an empty Config, so a caller can
// offer to create one; a present but unparseable file is an error naming the
// line, and is never treated as absent.
func Load(home string, lookup Lookup) (*Config, error) {
	path := Path(home, lookup)
	if path == "" {
		return nil, fmt.Errorf("%w: no HMD_HOME and no %s", ErrNoConfig, PathOverride)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w at %s", ErrNoConfig, path)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes the file's bytes.
//
// Unknown keys are refused rather than ignored. A misspelt `auth_uri` that
// parsed silently would produce "auth_url is required" against a file that
// visibly contains a URL, which is the least actionable error this package
// could emit.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return nil, decodeError(err)
	}
	for name, profile := range cfg.Profiles {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("a profile must have a name")
		}
		profile.Name = name
		cfg.Profiles[name] = profile
	}
	return &cfg, nil
}

// decodeError unwraps go-toml's rich error into a message carrying the
// offending line, which is the whole reason this package decodes with a
// Decoder rather than toml.Unmarshal.
func decodeError(err error) error {
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		return fmt.Errorf("unrecognised key:\n%s", strict.String())
	}
	var decode *toml.DecodeError
	if errors.As(err, &decode) {
		row, col := decode.Position()
		return fmt.Errorf("line %d, column %d: %s\n%s", row, col, decode.Error(), decode.String())
	}
	return err
}

// Profile resolves a profile by name.
//
// An empty name means the default: default_profile when set, or the only
// profile when the file defines exactly one. Guessing beyond that is refused
// -- picking one of three alphabetically would authenticate against an
// endpoint nobody named.
func (c *Config) Profile(name string) (Profile, error) {
	if c == nil || len(c.Profiles) == 0 {
		return Profile{}, ErrNoProfiles
	}
	if name == "" {
		name = c.DefaultProfile
	}
	if name == "" {
		if len(c.Profiles) == 1 {
			for only := range c.Profiles {
				name = only
			}
		} else {
			return Profile{}, fmt.Errorf(
				"no profile named and no default_profile set; pass --profile with one of: %s",
				strings.Join(c.ProfileNames(), ", "))
		}
	}
	profile, ok := c.Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("no profile %q; the file defines: %s",
			name, strings.Join(c.ProfileNames(), ", "))
	}
	profile.Name = name
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	return profile.withDefaults(), nil
}

// ProfileNames is every defined profile, sorted.
func (c *Config) ProfileNames() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Validate reports what a profile is missing.
func (p Profile) Validate() error {
	url := strings.TrimSpace(p.AuthURL)
	if url == "" {
		return fmt.Errorf("profile %q has no auth_url", p.Name)
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("profile %q: auth_url %q must be an http:// or https:// URL, "+
			"and must be the issuer rather than a bare hostname -- discovery is fetched from "+
			"%s/.well-known/openid-configuration", p.Name, p.AuthURL, strings.TrimRight(url, "/"))
	}
	// The service URLs are optional, so an unset one is not a problem; one that
	// is set and is not a URL is, and saying so here beats a connection error
	// naming a host nobody typed.
	for key, value := range map[string]string{
		"deployment_url":         p.DeploymentURL,
		"artifact_librarian_url": p.ArtifactLibrarianURL,
	} {
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
			return fmt.Errorf("profile %q: %s %q must be an http:// or https:// URL",
				p.Name, key, value)
		}
	}
	return nil
}

// withDefaults fills in what the file left out.
func (p Profile) withDefaults() Profile {
	p.AuthURL = strings.TrimRight(strings.TrimSpace(p.AuthURL), "/")
	p.DeploymentURL = trimSlash(p.DeploymentURL)
	p.ArtifactLibrarianURL = trimSlash(p.ArtifactLibrarianURL)
	p.CustomerCode = strings.TrimSpace(p.CustomerCode)
	p.Region = strings.TrimSpace(p.Region)
	if p.ClientID == "" {
		p.ClientID = DefaultClientID
	}
	if len(p.Scopes) == 0 {
		p.Scopes = append([]string(nil), DefaultScopes...)
	}
	return p
}

// Save writes the config.
//
// 0600, and the directory 0700: a profile may carry a client id, and the file
// sits beside hmd.env, which holds live secrets at the same mode.
func Save(home string, lookup Lookup, c *Config) error {
	path := Path(home, lookup)
	if path == "" {
		return fmt.Errorf("%w: no HMD_HOME and no %s", ErrNoConfig, PathOverride)
	}
	data, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	return atomicfile.Write(path, data, 0o600, 0o700)
}

// Set adds or replaces a profile, creating the config if there is none.
//
// The first profile written becomes the default, so a user who ran `nsctl
// login` once and answered one prompt never has to name a profile afterwards.
func (c *Config) Set(profile Profile) {
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	if c.DefaultProfile == "" {
		c.DefaultProfile = profile.Name
	}
	c.Profiles[profile.Name] = profile
}

// Example is a pasteable profile table, used by the errors that refuse to
// guess. An error that says what is missing without showing the shape sends
// the user to the documentation for two lines of TOML.
func Example(name string) string {
	if name == "" {
		name = "neuronsphere"
	}
	return fmt.Sprintf("default_profile = %q\n\n[profile.%s]\nauth_url = \"https://auth.example-admin-neuronsphere.io/oauth2/ns\"\n",
		name, name)
}
