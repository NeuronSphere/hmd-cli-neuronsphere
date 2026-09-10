package cpext

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/keyring"
)

// DefaultSecretsDir is where a delivered credential lands inside a container.
// A service opts in by declaring a tmpfs at this path.
const DefaultSecretsDir = "/run/ns-secrets"

// SecretEnvPrefix names the environment variable an env-delivered credential
// arrives as.
const SecretEnvPrefix = "NS_SECRET_"

// Delivery is how a resolved credential reaches the service.
type Delivery string

const (
	// DeliverFile writes the value into the tmpfs after the container starts.
	// Nothing is written under $HMD_HOME and nothing appears in
	// `docker inspect`. The default, and the one to use.
	DeliverFile Delivery = "file"
	// DeliverEnv puts the value in the container's environment. Visible in
	// `docker inspect` and to anything that can read the daemon socket.
	// Acceptable on a single-developer machine, recorded as the weaker option,
	// and never chosen when DeliverFile is available -- which is only when the
	// consuming service cannot read its configuration from a file.
	DeliverEnv Delivery = "env"
)

// Credential is one declared credential reference.
//
// The manifest names where a credential lives. It never carries one, and
// nothing nsctl does writes one under $HMD_HOME -- the same sentence that put
// the manifest in .config/ rather than .cache/ forbids it from holding a
// secret.
type Credential struct {
	Name string
	// Service is an explicit keychain service name, the shape NERD006 writes.
	Service string
	// URL is an index URL, whose four candidate service names are probed in
	// hmd-cli-tools' order when no explicit service resolves.
	URL string
	// User is the keychain account. Defaults to $USER.
	User     string
	Deliver  Delivery
	Resolved bool
	// Err is why this credential did not resolve. Never carries the value.
	Err error

	// value is the resolved secret. Unexported, and deliberately not reachable
	// from ConfigVars: interpolating it into the compose file would put it in
	// the config hash and therefore in a container label.
	value string
}

// Services is the keychain service names this credential is looked up under,
// in probe order: the explicit one the manifest named, then the candidates
// derived from any URL it named.
func (c Credential) Services() []string {
	var services []string
	if c.Service != "" {
		services = append(services, c.Service)
	}
	return append(services, keyring.Candidates(c.URL)...)
}

// Path is where a file-delivered credential lands inside the container.
func (c Credential) Path(secretsDir string) string { return path.Join(secretsDir, c.Name) }

// EnvVar is the variable an env-delivered credential arrives as.
func (c Credential) EnvVar() string { return SecretEnvPrefix + varName(c.Name) }

// credentialsFrom reads the `credentials` block out of an instance
// configuration.
func credentialsFrom(config map[string]any, lookup compose.Lookup) ([]Credential, error) {
	raw, ok := config["credentials"]
	if !ok {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("instance_configuration.credentials must be a list")
	}
	seen := map[string]bool{}
	creds := make([]Credential, 0, len(list))
	for i, item := range list {
		block, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("instance_configuration.credentials[%d] must be a block", i)
		}
		// ${VAR} is expanded here, so `keyring_user: "${USER}"` -- the shape
		// SPEC009 documents -- means the account rather than the literal.
		c := Credential{
			Name:    stringField(block, "name"),
			Service: expand(stringField(block, "keyring_service"), lookup),
			URL:     expand(stringField(block, "url"), lookup),
			User:    expand(stringField(block, "keyring_user"), lookup),
			Deliver: Delivery(stringField(block, "deliver")),
		}
		if c.Name == "" {
			return nil, fmt.Errorf("instance_configuration.credentials[%d] has no 'name'", i)
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("instance_configuration.credentials[%d]: duplicate name %q", i, c.Name)
		}
		seen[c.Name] = true
		if c.Service == "" && c.URL == "" {
			return nil, fmt.Errorf("credential %q names neither 'keyring_service' nor 'url', "+
				"so there is nothing to look up", c.Name)
		}
		if _, literal := block["password"]; literal {
			// Said plainly rather than ignored. A manifest is a file a
			// developer edits and may commit; a secret in one is a secret in a
			// git history.
			return nil, fmt.Errorf("credential %q carries a literal password. A manifest names where a "+
				"credential lives and never holds one; store it in the keychain and name it here", c.Name)
		}
		switch c.Deliver {
		case "":
			c.Deliver = DeliverFile
		case DeliverFile, DeliverEnv:
		default:
			return nil, fmt.Errorf("credential %q: unknown deliver %q; expected %q or %q",
				c.Name, c.Deliver, DeliverFile, DeliverEnv)
		}
		creds = append(creds, c)
	}
	sort.Slice(creds, func(i, j int) bool { return creds[i].Name < creds[j].Name })
	return creds, nil
}

// expand resolves ${VAR} against the extension's lookup, leaving a value with
// no variables in it untouched.
func expand(value string, lookup compose.Lookup) string {
	if value == "" || lookup == nil || !strings.Contains(value, "$") {
		return value
	}
	return os.Expand(value, lookup)
}

func stringField(block map[string]any, key string) string {
	v, _ := block[key].(string)
	return strings.TrimSpace(v)
}

// SecretsDir is where this extension's file-delivered credentials land.
func (e Extension) SecretsDir() string {
	if dir := stringField(e.Config, "secrets_dir"); dir != "" {
		return dir
	}
	return DefaultSecretsDir
}

// Resolver reads the host keychain. Narrowed to the one call cpext makes, so
// delivery is testable without one.
type Resolver interface {
	ResolveAny(ctx context.Context, services []string, username string) (string, error)
}

// ResolveCredentials looks each declared credential up in the host keychain.
//
// Never fatal, and never fatal per credential either: an unresolved credential
// is recorded on itself and the extension still starts. The alternative --
// refusing to start a registry because one private upstream has no token --
// takes away the cached public index too, which is the larger loss.
//
// Resolution happens on every start. A rotated keychain entry therefore takes
// effect on the next one; nothing watches the keychain.
func (e *Extension) ResolveCredentials(ctx context.Context, r Resolver, defaultUser string) {
	for i := range e.Credentials {
		c := &e.Credentials[i]
		user := c.User
		if user == "" {
			user = defaultUser
		}
		value, err := r.ResolveAny(ctx, c.Services(), user)
		if err != nil {
			// Scrubbed: a candidate service name can be an index URL, and an
			// index URL can carry userinfo.
			c.Err = fmt.Errorf("%s", keyring.Scrub(err.Error()))
			continue
		}
		c.value, c.Resolved = value, true
	}
}

// EnvDelivered is the environment a service takes for its env-delivered
// credentials.
//
// Applied before the container is created, because a container's environment
// is fixed at creation. The value therefore reaches the config hash, which is
// the one good thing about this path: a rotated credential recreates the
// container instead of being ignored until something restarts.
func (e Extension) EnvDelivered() map[string]string {
	env := map[string]string{}
	for _, c := range e.Credentials {
		if c.Deliver == DeliverEnv && c.Resolved {
			env[c.EnvVar()] = c.value
		}
	}
	return env
}

// FileDelivered are the credentials written into the tmpfs after start.
func (e Extension) FileDelivered() []Credential {
	var out []Credential
	for _, c := range e.Credentials {
		if c.Deliver == DeliverFile && c.Resolved {
			out = append(out, c)
		}
	}
	return out
}

// DeliverSecrets writes each file-delivered credential into the tmpfs of every
// one of this extension's containers that declares it.
//
// After the container is up, and on *every* start -- including one this call
// did not cause. A tmpfs is empty again after any restart, so a writer that ran
// only when a container was created would leave the service anonymous after a
// Docker-initiated restart, failing against its upstream in a way that reads as
// a missing package. This is the sharpest edge in the mechanism.
//
// A service that declares no tmpfs at the secrets directory is reported rather
// than skipped: a credential that resolved and was delivered nowhere is the
// silent half of the same failure.
func DeliverSecrets(ctx context.Context, w SecretWriter, e Extension, projectName string) []error {
	creds := e.FileDelivered()
	if len(creds) == 0 {
		return nil
	}
	dir := e.SecretsDir()

	var targets []compose.Service
	for _, s := range e.Services {
		if s.MountsTmpfs(dir) {
			targets = append(targets, s)
		}
	}
	if len(targets) == 0 {
		return []error{fmt.Errorf("%s resolved %s but no service declares a tmpfs at %s, "+
			"so there is nowhere to deliver them", e.Instance, plural(len(creds), "credential"), dir)}
	}

	var problems []error
	for _, s := range targets {
		name := s.Name(projectName)
		for _, c := range creds {
			if err := w.WriteInto(ctx, name, c.Path(dir), []byte(c.value)); err != nil {
				// One problem per credential, not per container run: which
				// credential failed is what the reader acts on. Scrubbed for
				// the reason the resolve path is -- a path or an error can
				// carry a URL, and a URL can carry userinfo.
				problems = append(problems, fmt.Errorf("delivering %s/%s to %s: %s",
					e.Instance, c.Name, name, keyring.Scrub(err.Error())))
			}
		}
	}
	return problems
}

// SecretWriter writes one file inside a running container, narrowed so the
// delivery logic is testable without a daemon.
type SecretWriter interface {
	WriteInto(ctx context.Context, name, path string, data []byte) error
}

// CredentialReport is what `status` and `apply` say about a credential,
// without ever saying the value.
//
// Which references resolved and which did not is the part that matters:
// without it an unresolved credential surfaces as a 401 from a cache, which
// looks exactly like an empty index.
func CredentialReport(exts []Extension) []string {
	var lines []string
	for _, e := range exts {
		for _, c := range e.Credentials {
			switch {
			case c.Resolved:
				lines = append(lines, fmt.Sprintf("%s/%s resolved from %s", e.Instance, c.Name, c.sourceName()))
			default:
				// Where it looked, not only that it failed. "no credential
				// stored" on its own leaves the reader guessing which of four
				// service names to add an entry under.
				lines = append(lines, fmt.Sprintf("%s/%s did not resolve from %s: %v",
					e.Instance, c.Name, c.sourceName(), c.Err))
			}
		}
	}
	return lines
}

// sourceName is where the credential was looked for, never what was found.
func (c Credential) sourceName() string {
	if c.Service != "" {
		return c.Service
	}
	return keyring.SafeURL(c.URL)
}
