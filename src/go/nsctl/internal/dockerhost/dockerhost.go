// Package dockerhost resolves the container engine endpoint nsctl talks to.
//
// The docker CLI resolves DOCKER_HOST, then DOCKER_CONTEXT, then the current
// context recorded in ~/.docker/config.json. The Engine API Go client's
// client.FromEnv resolves DOCKER_HOST and nothing else, and otherwise falls
// back to unix:///var/run/docker.sock.
//
// So the two halves of nsctl -- internal/container, which shells out, and
// internal/compose, which uses the Engine API -- can address two different
// daemons. That is not a gap in support for some new runtime: Docker Desktop's
// own context names unix://$HOME/.docker/run/docker.sock, and the Engine API
// path works there only because Docker Desktop installs a compatibility
// symlink at the legacy path. Colima installs no such symlink, so the same
// code reached nothing, passed a CLI-shaped preflight, and failed four steps
// later creating the network.
//
// NERD021.
package dockerhost

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/docker/docker/client"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
)

// Source records how an endpoint was chosen, so a message can name it. The
// failure this package exists for is one where the host looked plausible and
// the route to it was wrong, so what chose it is half the diagnosis.
type Source string

const (
	// SourceEnvHost means DOCKER_HOST named it.
	SourceEnvHost Source = "the DOCKER_HOST environment variable"
	// SourceContext means a docker context named it.
	SourceContext Source = "the docker context"
	// SourceDefaultSocket means nothing named it and this is the fallback --
	// the case that produced the original bug report.
	SourceDefaultSocket Source = "the default socket"
)

// ErrUnsupportedScheme is returned for an endpoint nsctl cannot dial itself.
var ErrUnsupportedScheme = errors.New("unsupported docker endpoint scheme")

// Endpoint is a resolved daemon endpoint.
type Endpoint struct {
	// Host is a dialable endpoint: unix://, tcp:// or npipe://.
	Host string
	// Context is the docker context that named it, empty when DOCKER_HOST did
	// or when nothing did.
	Context string
	Source  Source
	// SkipTLSVerify is the context's own setting, carried for diagnostics.
	SkipTLSVerify bool
}

// Describe renders the endpoint for a message. It always names both the host
// and how nsctl arrived at it.
func (e Endpoint) Describe() string {
	switch {
	case e.Source == SourceContext && e.Context != "":
		return fmt.Sprintf("%s (the docker context %q)", e.Host, e.Context)
	default:
		return fmt.Sprintf("%s (%s)", e.Host, e.Source)
	}
}

// DefaultHost is the socket the Engine API client falls back to, and the one
// the original bug report named.
func DefaultHost() string {
	if runtime.GOOS == "windows" {
		return "npipe:////./pipe/docker_engine"
	}
	return "unix:///var/run/docker.sock"
}

// Inspector reports the daemon endpoint the docker CLI resolves, in the
// "<context>\t<host>\t<skipTLS>" form ContextEndpoint produces. A test
// supplies its own.
type Inspector func(ctx context.Context) (string, error)

// Resolver resolves the endpoint at most once. The zero value is usable.
type Resolver struct {
	// Inspect asks the docker CLI. Nil means the real one.
	Inspect Inspector
	// Lookup reads the environment. Nil means os.Getenv.
	Lookup hmdenv.Lookup

	once sync.Once
	ep   Endpoint
	err  error
}

func (r *Resolver) lookup(key string) string {
	if r.Lookup == nil {
		return os.Getenv(key)
	}
	return r.Lookup(key)
}

// Resolve answers where the engine is.
//
// The returned error is diagnostic, never fatal: when the CLI cannot be asked,
// the endpoint falls back to what the Engine API client would have used
// anyway, so this can only ever improve on the previous behaviour. The caller
// reports the error alongside the endpoint rather than refusing on it.
func (r *Resolver) Resolve(ctx context.Context) (Endpoint, error) {
	r.once.Do(func() { r.ep, r.err = r.resolve(ctx) })
	return r.ep, r.err
}

func (r *Resolver) resolve(ctx context.Context) (Endpoint, error) {
	inspect := r.Inspect
	envHost := r.lookup("DOCKER_HOST")

	if inspect != nil {
		if out, err := inspect(ctx); err == nil {
			if ep, ok := parseContextEndpoint(out); ok {
				// The CLI reports context "default" carrying DOCKER_HOST's
				// value when the variable is set, so the source is derived
				// from the value rather than asked for separately.
				if envHost != "" && ep.Host == envHost {
					ep.Source, ep.Context = SourceEnvHost, ""
				}
				return ep, nil
			}
		} else {
			// The CLI's own message names the context and the file it looked
			// for, which is better than anything written here.
			if envHost != "" {
				return Endpoint{Host: envHost, Source: SourceEnvHost}, err
			}
			return Endpoint{Host: DefaultHost(), Source: SourceDefaultSocket}, err
		}
	}

	if envHost != "" {
		return Endpoint{Host: envHost, Source: SourceEnvHost}, nil
	}
	return Endpoint{Host: DefaultHost(), Source: SourceDefaultSocket}, nil
}

// parseContextEndpoint reads the tab-separated form ContextEndpoint emits.
//
// Split out as a pure function so the format is tested without a docker
// binary, the way parseState and parseContainerIP already are.
func parseContextEndpoint(out string) (Endpoint, bool) {
	// Trimmed of line endings only, not of all whitespace: TrimSpace on the
	// whole string swallows an empty leading field and shifts every column,
	// turning the host into the context name.
	fields := strings.Split(strings.Trim(out, "\r\n"), "\t")
	if len(fields) < 2 || strings.TrimSpace(fields[1]) == "" {
		return Endpoint{}, false
	}
	ep := Endpoint{
		Context: strings.TrimSpace(fields[0]),
		Host:    strings.TrimSpace(fields[1]),
		Source:  SourceContext,
	}
	if len(fields) > 2 {
		ep.SkipTLSVerify = strings.TrimSpace(fields[2]) == "true"
	}
	return ep, true
}

// ClientOpts renders the endpoint as Engine API client options.
//
// An ssh:// endpoint is refused rather than dialled: reaching it needs the
// docker CLI's own connection helper, which lives in github.com/docker/cli --
// the dependency NERD021 SPEC002 declines. Today it fails anyway, by dialling
// the SSH port and speaking HTTP at it; a named refusal is the improvement.
func (e Endpoint) ClientOpts(lookup hmdenv.Lookup) ([]client.Opt, error) {
	if lookup == nil {
		lookup = os.Getenv
	}
	scheme := schemeOf(e.Host)
	switch scheme {
	case "unix", "npipe", "tcp", "http", "https":
	case "ssh":
		return nil, fmt.Errorf("%w: nsctl cannot use the ssh:// docker endpoint %s: "+
			"reaching it needs the docker CLI's own connection helper, which nsctl does not "+
			"embed. Point nsctl at a local engine (`docker context use <name>`) or set "+
			"DOCKER_HOST to a directly reachable endpoint", ErrUnsupportedScheme, e.Describe())
	default:
		return nil, fmt.Errorf("%w: nsctl does not know how to reach %s", ErrUnsupportedScheme, e.Describe())
	}

	opts := []client.Opt{client.WithHost(e.Host), client.WithAPIVersionNegotiation()}
	if scheme == "tcp" || scheme == "https" {
		if lookup("DOCKER_CERT_PATH") != "" || lookup("DOCKER_TLS_VERIFY") != "" {
			opts = append(opts, client.WithTLSClientConfigFromEnv())
		}
	}
	return opts, nil
}

func schemeOf(host string) string {
	u, err := url.Parse(host)
	if err != nil || u.Scheme == "" {
		return ""
	}
	return u.Scheme
}
