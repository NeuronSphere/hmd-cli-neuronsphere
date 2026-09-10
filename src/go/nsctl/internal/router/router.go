// Package router generates hmd_proxy's nginx configuration.
//
// hmd_proxy is the only container that publishes host ports. Everything else --
// the control-plane microservices, every environment's services, Trino, the
// Argo UI -- is reached through it, either as an HTTP location or, for
// non-HTTP protocols, as an L4 stream listener.
//
// Routes are assembled from fragments rather than one generated file, so each
// owner writes exactly one file and deleting an environment is an unlink:
//
//	$HMD_HOME/.cache/nginx/
//	    neuronsphere.conf             # static shell, generated once per start
//	    http.d/00-control-plane.conf  # /hmd_ms_deployment/, /hmd_ms_naming/, ...
//	    http.d/10-env-<slug>.conf     # /<slug>/<service>/
//	    stream.d/00-control-plane.conf
//	    vhost.d/00-control-plane.conf
//
// The whole directory is bind-mounted into hmd_proxy at /etc/nginx/ns, so
// adding or removing a fragment needs only a reload, never a restart.
//
// Postgres and JanusGraph are deliberately not streamed: databases are not
// reachable from the host. Use `docker exec <container> psql`.
package router

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// NSDir is where the fragment directory is mounted inside hmd_proxy.
const NSDir = "/etc/nginx/ns"

// ProxyContainer is the nginx container, overridable with
// HMD_LOCAL_PROXY_CONTAINER.
const ProxyContainer = "hmd_proxy"

// ControlPlaneFragment is the filename every control-plane fragment uses. The
// numeric prefix orders it ahead of the environment fragments.
const ControlPlaneFragment = "00-control-plane.conf"

// EnvFragmentPrefix names an environment's fragments.
const EnvFragmentPrefix = "10-env-"

// FlociStreamPort is the port hmd_proxy streams to the control-plane Floci.
// localhost:4566 is this stream and nothing else.
const FlociStreamPort = 4566

// GUIUpstream is the Deployment GUI container and the port it listens on.
const GUIUpstream = "hmd_deployment_gui:8000"

// AuthUpstream is the mock identity provider container and its port.
const AuthUpstream = "hmd_authd:8080"

// DefaultResolver is Docker's embedded DNS.
const DefaultResolver = "127.0.0.11"

// DefaultSigV4Region is the region in the injected credential scope.
const DefaultSigV4Region = "us-west-2"

// AltAuthHeader carries the caller's own Authorization past the account
// selector.
//
// An environment route has to spend Authorization on Floci's SigV4 credential
// scope -- a 12-digit access key *is* the account, and it is the only thing
// Floci resolves a v1 REST API's owner from. A caller's bearer token would
// displace it, so the token rides here instead and hmd-lib-auth's auth_token()
// falls back to it when Authorization holds a credential scope. In the cloud
// this header does not exist and Authorization is never a credential scope, so
// nothing there behaves differently.
const AltAuthHeader = "X-NS-Authorization"

// serviceAliases route alternate spellings to the same gateway. The robot
// suites build their URL from HMD_INSTANCE_NAME, which arrives as any of these
// depending on how bender is invoked.
var serviceAliases = map[string][]string{
	"hmd_ms_deployment": {"ms-deployment", "ms_deployment", "hmd-ms-deployment"},
	"hmd_ms_naming":     {"ms-naming", "ms_naming", "hmd-ms-naming"},
}

// Lookup resolves an environment variable, returning "" when unset.
type Lookup = func(string) string

// Router writes fragments under one HMD_HOME.
type Router struct {
	Home   string
	Lookup Lookup
}

// New builds a Router for an HMD_HOME.
func New(home string, lookup Lookup) *Router {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	return &Router{Home: home, Lookup: lookup}
}

// CacheDir is $HMD_HOME/.cache/nginx, the directory bind-mounted into hmd_proxy.
func (r *Router) CacheDir() string { return filepath.Join(r.Home, ".cache", "nginx") }

// BaseConfigPath is the static shell nginx loads.
func (r *Router) BaseConfigPath() string { return filepath.Join(r.CacheDir(), "neuronsphere.conf") }

// HTTPDir, StreamDir and VhostDir hold the fragments.
func (r *Router) HTTPDir() string   { return filepath.Join(r.CacheDir(), "http.d") }
func (r *Router) StreamDir() string { return filepath.Join(r.CacheDir(), "stream.d") }
func (r *Router) VhostDir() string  { return filepath.Join(r.CacheDir(), "vhost.d") }

func (r *Router) resolver() string {
	res := r.Lookup("HMD_LOCAL_NGINX_RESOLVER")
	if res == "" {
		res = DefaultResolver
	}
	return fmt.Sprintf("resolver %s valid=10s ipv6=off;", res)
}

func (r *Router) sigV4Region() string {
	if v := r.Lookup("AWS_DEFAULT_REGION"); v != "" {
		return v
	}
	return DefaultSigV4Region
}

// ProxyContainerName is the nginx container to exec into.
func (r *Router) ProxyContainerName() string {
	if v := r.Lookup("HMD_LOCAL_PROXY_CONTAINER"); v != "" {
		return v
	}
	return ProxyContainer
}

// EnsureDirs creates the fragment directories. A glob matching no files is
// legal in nginx, so the base config is valid before any fragment exists.
func (r *Router) EnsureDirs() error {
	for _, dir := range []string{r.HTTPDir(), r.StreamDir(), r.VhostDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	return nil
}

// BaseConfig renders the static shell that includes every fragment.
//
// vhost.d is included at the http level, not inside the server block: a
// Host-routed UI needs a whole server block, whereas an http.d fragment is a
// location spliced into the one path-routed server. Keeping default_server on
// that server is what preserves the existing behaviour -- a request reaches a
// vhost only when its Host actually matches.
func (r *Router) BaseConfig() string {
	return fmt.Sprintf(`events {
    worker_connections 1024;
}
http {
    map $http_upgrade $connection_upgrade {
        default upgrade;
        ''      close;
    }
    include %s/vhost.d/*.conf;
    server {
        listen 80 default_server;
        server_name _;
        include %s/http.d/*.conf;
        location / {
            return 404 '{"error": "no route defined"}';
        }
    }
}
stream {
    %s
    include %s/stream.d/*.conf;
}
`, NSDir, NSDir, r.resolver(), NSDir)
}

// WriteBaseConfig writes the shell. It is rewritten on every start so an
// upgrade picks up shell changes; the fragments carry the actual routes.
func (r *Router) WriteBaseConfig() error {
	if err := r.EnsureDirs(); err != nil {
		return err
	}
	return writeFile(r.BaseConfigPath(), r.BaseConfig())
}

// BootstrapConfig is the self-contained config hmd_proxy starts on, before any
// route exists.
//
// It must not include the fragment directories: the bind-mounted directory may
// not be visible inside the container yet, and stale fragments from a previous
// run can reference containers that are not up.
//
// It must, however, already listen on 4566. Floci publishes no host port of its
// own -- localhost:4566 is this stream and nothing else -- and `start` polls
// that address to decide whether Floci came up. A placeholder without it
// guarantees a five-minute timeout and a degraded start no matter how healthy
// Floci actually is.
//
// flociHost must be an explicit network alias, never a compose service key:
// compose registers every service key as an alias on the shared network, so
// `floci` can resolve to more than one container.
func (r *Router) BootstrapConfig(flociHost string) string {
	return fmt.Sprintf(`events {}
http {
    server {
        listen 80 default_server;
        server_name _;
        location / { return 503 'NeuronSphere is starting...'; }
    }
}
stream {
    %s
%s
}
`, r.resolver(), indent(streamServer(FlociStreamPort, flociHost+":4566", "ns_floci"), 4))
}

// ConfigServesFloci reports whether the config at path results in something
// listening on 4566.
//
// Two ways it can: the listener is inline (the bootstrap placeholder), or the
// config includes stream.d *and* the control-plane fragment carrying it is
// actually on disk. The second half matters -- an include-based config whose
// fragment directory was wiped looks complete but serves nothing on 4566, which
// is precisely the state that hangs a start.
func (r *Router) ConfigServesFloci(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	text := string(data)
	if strings.Contains(text, "listen 4566;") {
		return true
	}
	if !strings.Contains(text, NSDir+"/stream.d/") {
		return false
	}
	fragment, err := os.ReadFile(filepath.Join(r.StreamDir(), ControlPlaneFragment))
	if err != nil {
		return false
	}
	return strings.Contains(string(fragment), "listen 4566;")
}

// WriteBootstrapConfig writes the placeholder so hmd_proxy can start.
//
// A config that already serves 4566 is left alone: on a normal restart the full
// include-based config is on disk and still correct, and replacing it with a
// placeholder would drop every route until the reload later in the start. One
// that does not serve 4566 is overwritten even if it exists -- that is either
// the pre-multi-environment single-file config or an older placeholder, and
// keeping it is what makes a start hang waiting for a port nobody listens on.
func (r *Router) WriteBootstrapConfig(flociHost string) (bool, error) {
	path := r.BaseConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if r.ConfigServesFloci(path) {
		return false, nil
	}
	return true, writeFile(path, r.BootstrapConfig(flociHost))
}

// WriteControlPlaneRoutes writes http.d/00-control-plane.conf.
//
// services maps a service name to its API Gateway id; extra maps a path to an
// upstream URL for routes that bypass API Gateway.
func (r *Router) WriteControlPlaneRoutes(services map[string]string, stage, upstreamHost string, extra map[string]string) error {
	if stage == "" {
		stage = "local"
	}
	if upstreamHost == "" {
		upstreamHost = "http://neuronsphere:4566"
	}

	var blocks []string
	routed := map[string]bool{}
	for _, name := range sortedKeys(services) {
		blocks = append(blocks, wrap(name, r.apiLocation(name, upstreamHost, services[name], stage, "")))
		routed[name] = true
	}
	for _, canonical := range sortedKeys(serviceAliases) {
		gw, ok := services[canonical]
		if !ok {
			continue
		}
		for _, alias := range serviceAliases[canonical] {
			if routed[alias] {
				continue
			}
			blocks = append(blocks, wrap(alias, r.apiLocation(alias, upstreamHost, gw, stage, "")))
			routed[alias] = true
		}
	}
	for _, path := range sortedKeys(extra) {
		clean := strings.Trim(path, "/")
		blocks = append(blocks, wrap(clean, passthroughLocation(clean, extra[path])))
	}

	return r.writeFragment(filepath.Join(r.HTTPDir(), ControlPlaneFragment), blocks, "control-plane HTTP routes")
}

// WriteControlPlaneStreams writes stream.d/00-control-plane.conf.
//
// Only Floci is streamed. It carries :4566 so that neuronsphere:4566 -- the
// hostname baked into presigned S3 and API URLs, mapped to 127.0.0.1 in the
// host's /etc/hosts -- keeps working now that the Floci container publishes
// nothing itself.
func (r *Router) WriteControlPlaneStreams(flociHost string) error {
	block := wrap("floci", streamServer(FlociStreamPort, flociHost+":4566", "ns_floci"))
	return r.writeFragment(filepath.Join(r.StreamDir(), ControlPlaneFragment), []string{block}, "control-plane streams")
}

// ControlPlaneVhosts is what the control plane wants served by name or by port.
type ControlPlaneVhosts struct {
	// GUIEnabled and GUIPort serve the Deployment GUI at the root path of a
	// published host port rather than behind the wildcard
	// *.<slug>.neuronsphere.io vhost, so reaching it costs no /etc/hosts entry.
	GUIEnabled bool
	GUIPort    int
	// AuthHost is the hostname the mock identity provider is reached at, or ""
	// when it is not enabled.
	//
	// By name, not by port, and that is forced. The issuer is the base URL
	// every consumer appends to -- `{issuer}/v1/keys`, `{issuer}/v1/token` --
	// and it must be one string whether reached from the browser, from a pod
	// through CoreDNS, or from a Floci container through a Docker alias. A
	// hostname can be pointed at hmd_proxy in all three places; a port on
	// localhost cannot.
	AuthHost string
}

// WriteControlPlaneVhosts writes vhost.d/00-control-plane.conf.
//
// Always written in full: a service that is no longer enabled loses its block
// by being absent, which is what removes a listener a previous run added.
func (r *Router) WriteControlPlaneVhosts(v ControlPlaneVhosts) error {
	var blocks []string
	if v.GUIEnabled {
		blocks = append(blocks, wrap("deployment-gui", r.containerVhostServer(v.GUIPort, GUIUpstream, "ns_gui")))
	}
	if v.AuthHost != "" {
		blocks = append(blocks, wrap("authd", r.namedVhostServer(v.AuthHost, AuthUpstream, "ns_authd")))
	}
	return r.writeFragment(filepath.Join(r.VhostDir(), ControlPlaneFragment), blocks, "control-plane vhosts")
}

// ExtensionFragment is the vhost fragment holding every control-plane
// extension's server block. Numbered after the control plane's own so that a
// hand-read of vhost.d/ shows the platform first and additions after it.
const ExtensionFragment = "20-extensions.conf"

// ExtensionVhost is one control-plane extension served by name (NERD004
// SPEC007): Host is the hostname derived from its single configured URL, and
// Upstream the "<container>:<port>" that answers for it.
type ExtensionVhost struct {
	Name     string
	Host     string
	Upstream string
}

// WriteExtensionVhosts writes vhost.d/20-extensions.conf.
//
// Written whole on every start, like WriteControlPlaneVhosts and for the same
// reason: an extension that is no longer declared loses its listener by being
// absent. An empty set therefore writes an empty fragment rather than leaving
// the previous one in place.
func (r *Router) WriteExtensionVhosts(vhosts []ExtensionVhost) error {
	var blocks []string
	for _, v := range vhosts {
		if v.Host == "" || v.Upstream == "" {
			continue
		}
		blocks = append(blocks, wrap("extension "+v.Name,
			r.namedVhostServer(v.Host, v.Upstream, "ns_ext_"+varSafe(v.Name))))
	}
	return r.writeFragment(filepath.Join(r.VhostDir(), ExtensionFragment), blocks, "control-plane extension vhosts")
}

// varSafe renders a name as an nginx variable name component. An instance name
// may hold dots and dashes; an nginx variable may not.
func varSafe(name string) string {
	var b strings.Builder
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// namedVhostServer serves one container on port 80 for a single Host.
//
// The upstream is deferred through a resolver variable for the same reason
// containerVhostServer does it, and here it matters more: a literal proxy_pass
// host is resolved once at config load, so a name that does not resolve makes
// `nginx -t` fail and the reload is rejected *whole* -- taking every other
// route down with it. The identity provider is optional and off by default, so
// its container is routinely absent, and an absent optional service must cost
// its own 502 and nothing else.
func (r *Router) namedVhostServer(serverName, upstream, varName string) string {
	return fmt.Sprintf(`server {
    listen 80;
    server_name %s;
    %s
    set $%s "%s";
    location / {
        proxy_pass http://$%s;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_http_version 1.1;
        proxy_read_timeout 300s;
        proxy_connect_timeout 75s;
    }
}`, serverName, r.resolver(), varName, upstream, varName)
}

// containerVhostServer is a port-listening server block proxying to a container
// by name.
//
// The upstream goes through a variable and a resolver for the reason
// resolver() gives: a literal proxy_pass host:port is resolved once, at config
// load, and a name that does not resolve makes `nginx -t` fail -- which would
// reject the whole reload and take every *other* route down with it. Deferring
// the lookup to the request means a GUI container that failed to start costs
// only its own 502.
//
// Host is forwarded verbatim: nothing downstream selects on it (there is no
// Ingress here) and localhost already satisfies the app's allowed-hosts list.
func (r *Router) containerVhostServer(port int, upstream, varName string) string {
	return fmt.Sprintf(`server {
    listen %d;
    server_name _;
    %s
    set $%s "%s";
    location / {
        proxy_pass http://$%s;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_read_timeout 300s;
        proxy_connect_timeout 75s;
    }
}`, port, r.resolver(), varName, upstream, varName)
}

func (r *Router) writeFragment(path string, blocks []string, header string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	body := fmt.Sprintf("# %s\n# Generated by hmd-cli-neuronsphere. Do not edit.\n%s", header, strings.Join(blocks, "\n"))
	return writeFile(path, body)
}

// -- blocks ---------------------------------------------------------------

// apiLocation proxies /<path>/ to an API Gateway stage.
//
// The trailing slash on both the location and the proxy_pass target makes nginx
// strip the prefix, so the Lambda receives clean /api/... paths rather than
// /<path>/api/... (which FastAPI would 404).
//
// accountID is required for any API owned by a non-default Floci account -- so,
// every named environment. One Floci serves all accounts and resolves which one
// from the SigV4 credential scope, but proxy_pass issues an *unsigned* request,
// so there is nothing to resolve from and the invocation lands in the default
// account. The REST API is not there and Floci answers 404: indistinguishable
// from a route that was never wired.
//
// The credential scope is set *unconditionally*, displacing whatever the caller
// sent. It used to fill only an empty Authorization, which made the two uses of
// that header mutually exclusive: a bender request carrying a real JWT kept it,
// left Floci nothing to resolve the account from, and 404d -- so only the
// environment whose account happens to be Floci's default could serve an
// authenticated request at all. The caller's token travels beside it in
// AltAuthHeader, which hmd-lib-auth reads back.
func (r *Router) apiLocation(path, upstreamHost, gwID, stage, accountID string) string {
	account := ""
	if accountID != "" {
		account = fmt.Sprintf(
			"\n    proxy_set_header Authorization %q;\n    proxy_set_header %s $http_authorization;",
			sigV4Credential(accountID, r.sigV4Region()), AltAuthHeader)
	}
	return fmt.Sprintf(`location /%s/ {
    proxy_pass %s/restapis/%s/%s/_user_request_/;%s
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_read_timeout 300s;
    proxy_connect_timeout 75s;
}`, path, upstreamHost, gwID, stage, account)
}

// sigV4Credential is the credential scope Floci reads the account out of.
//
// Only the account is read; the date and region are structural filler, kept
// constant so the rendered config is stable. Floci does not verify the
// signature, so a static Signature=x suffices.
func sigV4Credential(accountID, region string) string {
	return fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/20200101/%s/execute-api/aws4_request, SignedHeaders=host, Signature=x",
		accountID, region)
}

// passthroughLocation proxies /<path>/ straight at a URL, with no API Gateway.
//
// client_max_body_size 0 -- unlimited -- because the one consumer is the DAG
// runner, and a submission carries the whole workflow manifest: every node's
// script, configuration and dependency edges. An environment declaring 28
// instances exceeds nginx's 1 MB default, and the failure is an nginx 413 the
// runner never sees, so nothing upstream can report it usefully.
func passthroughLocation(path, upstream string) string {
	return fmt.Sprintf(`location /%s/ {
    proxy_pass %s/;
    client_max_body_size 0;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Host $host;
    proxy_read_timeout 300s;
    proxy_connect_timeout 75s;
}`, path, strings.TrimRight(upstream, "/"))
}

// streamServer is a stream listener whose upstream resolves per connection.
//
// nginx resolves a literal proxy_pass host:port once, when the config loads,
// and refuses to start if the name does not resolve. Every stream upstream here
// is a container that may legitimately be down at that moment -- an environment
// that is not running, or the control-plane Floci during the same start that
// brings the proxy up. Resolving through a variable defers the lookup to the
// connection, so one stopped container can neither prevent nginx from starting
// nor take every other route down with it.
//
// varName must be unique per server block within one stream context.
func streamServer(port int, upstream, varName string) string {
	return fmt.Sprintf("server {\n    listen %d;\n    set $%s \"%s\";\n    proxy_pass $%s;\n}", port, varName, upstream, varName)
}

func markers(route string) (string, string) {
	return "# >>> ns route " + route + " >>>", "# <<< ns route " + route + " <<<"
}

func wrap(route, block string) string {
	begin, end := markers(route)
	return begin + "\n" + block + "\n" + end + "\n"
}

func indent(block string, spaces int) string {
	pad := strings.Repeat(" ", spaces)
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = pad + line
		}
	}
	return strings.Join(lines, "\n")
}

func writeFile(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// sortedKeys gives a deterministic iteration order, so a regenerated fragment
// is byte-identical when nothing changed and a diff means something did.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// StreamVarName builds a config-safe nginx variable name from a route identity.
func StreamVarName(parts ...string) string {
	var b strings.Builder
	b.WriteString("ns_")
	for i, p := range parts {
		if i > 0 {
			b.WriteByte('_')
		}
		for _, c := range p {
			switch {
			case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
				b.WriteRune(c)
			default:
				b.WriteByte('_')
			}
		}
	}
	return b.String()
}

// -- reload ---------------------------------------------------------------

// Execer runs a command inside a container and returns its combined output.
type Execer func(ctx context.Context, container string, args ...string) ([]byte, error)

// Reload validates the config and then reloads nginx.
//
// `nginx -t` runs first because a reload with a bad config leaves the
// *previous* config serving with only a message on stderr -- which surfaces
// much later as every route 404ing, with nothing pointing at the cause.
//
// A failure is returned rather than swallowed, but callers treat it as a
// warning: a proxy that did not reload still serves its old routes, which is
// better than aborting a start.
func (r *Router) Reload(ctx context.Context, exec Execer) error {
	name := r.ProxyContainerName()
	if out, err := exec(ctx, name, "nginx", "-t"); err != nil {
		return fmt.Errorf("the nginx configuration is invalid, so it was not reloaded: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec(ctx, name, "nginx", "-s", "reload"); err != nil {
		return fmt.Errorf("reloading nginx: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
