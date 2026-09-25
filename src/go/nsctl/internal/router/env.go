package router

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dnsd"
)

// Ports and names the environment routes use.
const (
	// K3sAPIPort is the k3s API server's port inside its container.
	K3sAPIPort = 6443
	// TrinoNodePort and TraefikNodePort front in-cluster ClusterIP services.
	// A NodePort is how anything on the Docker network reaches a ClusterIP
	// inside k3s: hmd_proxy connects to floci-eks-<cluster>:<nodePort>.
	TrinoNodePort   = 31880
	TraefikNodePort = 31080
	// LegacyTrinoHostPort is the historical fixed Trino port the default
	// environment additionally keeps, so existing integration tests need no
	// change.
	LegacyTrinoHostPort = 18080
	// IngressDomain is the suffix every local hostname is reached under. One
	// suffix, so one resolver arrangement covers the lot (NERD026 SPEC001).
	IngressDomain = dnsd.DefaultSuffix
	// HelmLocalSlug is the literal string hmd-cli-helm puts in every local
	// chart's alb.hostname, in *every* environment. It is not ours and cannot be
	// derived from: the deployed Ingress object's host is rewritten to the shape
	// below instead (NERD025 SPEC005).
	HelmLocalSlug = "local"
	// HelmIngressDomain is the suffix hmd-cli-helm renders, which is what an
	// Ingress object carries before it is rewritten.
	HelmIngressDomain = "local.neuronsphere.io"
)

// Env is what the router needs to know about an environment.
type Env struct {
	Slug      string
	AccountID string
	// TrinoPort and K3sPort come from the registry's slot arithmetic; both
	// must fall inside the band hmd_proxy publishes or the listener is
	// unreachable from the host.
	TrinoPort int
	K3sPort   int
	IsDefault bool
}

// EnvFragmentName is the file an environment owns in each fragment directory.
// Deleting an environment is an unlink.
func EnvFragmentName(slug string) string { return EnvFragmentPrefix + slug + ".conf" }

// FlociUpstreamHost is the in-network Floci every environment shares.
//
// Always the explicit network alias, never the compose service key: compose
// registers every service key as an alias on the shared network, so `floci` can
// resolve to more than one container. The host only selects the container --
// which *account* a request lands in comes from the credential scope, which is
// why the routes below set one.
const FlociUpstreamHost = "http://neuronsphere:4566"

// TrinoNodePortService and the Traefik NodePort are applied as separate
// services rather than by mutating the workload's own: Traefik's service is
// owned by a k3s Addon, which reverts out-of-band edits.
const (
	TrinoNodePortService   = "trino-local-nodeport"
	TraefikNodePortService = "traefik-local-nodeport"
	TraefikNamespace       = "kube-system"
)

// StreamEntry is one host port and the upstream it streams to.
type StreamEntry struct {
	Port     int
	Upstream string
}

// WriteEnvRoutes writes an environment's HTTP fragment: /<slug>/<service>/ per
// service.
func (r *Router) WriteEnvRoutes(env Env, services map[string]string, stage string, extra map[string]string) error {
	if stage == "" {
		stage = "local"
	}
	var blocks []string
	for _, name := range sortedKeys(services) {
		route := env.Slug + "/" + name
		blocks = append(blocks, wrap(route, r.apiLocation(route, FlociUpstreamHost, services[name], stage, env.AccountID)))
	}
	for _, path := range sortedKeys(extra) {
		route := env.Slug + "/" + strings.Trim(path, "/")
		blocks = append(blocks, wrap(route, passthroughLocation(route, extra[path])))
	}
	return r.writeFragment(filepath.Join(r.HTTPDir(), EnvFragmentName(env.Slug)), blocks,
		fmt.Sprintf("environment %q HTTP routes", env.Slug))
}

// WriteEnvStreams writes an environment's stream fragment.
//
// There is no per-environment Floci listener: one Floci serves every account
// and is already streamed on the control plane's :4566. Host-side callers pick
// the account with their credentials, not with a port.
func (r *Router) WriteEnvStreams(env Env, entries []StreamEntry) error {
	var blocks []string
	for _, e := range entries {
		name := env.Slug + ":" + strconv.Itoa(e.Port)
		blocks = append(blocks, wrap(name, streamServer(e.Port, e.Upstream, StreamVarName(env.Slug, strconv.Itoa(e.Port)))))
	}
	return r.writeFragment(filepath.Join(r.StreamDir(), EnvFragmentName(env.Slug)), blocks,
		fmt.Sprintf("environment %q streams", env.Slug))
}

// EnvStreamEntries assembles an environment's full stream listener list.
//
// Every writer rewrites the whole fragment, so a route only one of them knows
// about would be dropped by the next: wiring k3s and then deploying Trino would
// silently take kubectl offline again. Both upstreams are therefore passed
// through on every rewrite, and a caller that only knows one still has to
// supply the other.
func EnvStreamEntries(env Env, trinoUpstream, k3sUpstream string) []StreamEntry {
	var entries []StreamEntry
	if trinoUpstream != "" {
		entries = append(entries, StreamEntry{Port: env.TrinoPort, Upstream: trinoUpstream})
		if env.IsDefault && env.TrinoPort != LegacyTrinoHostPort {
			entries = append(entries, StreamEntry{Port: LegacyTrinoHostPort, Upstream: trinoUpstream})
		}
	}
	if k3sUpstream != "" {
		entries = append(entries, StreamEntry{Port: env.K3sPort, Upstream: k3sUpstream})
	}
	return entries
}

// IngressHostFor is the hostname an instance's user interface is reached at.
//
// The environment is its own dot-separated label, and the default environment
// has none:
//
//	airflow.ns.local        the default environment
//	airflow.dev2.ns.local   the environment `dev2`
//
// Two environments deploying the same chart therefore no longer claim one
// hostname between them, which they did for as long as this existed -- and only
// the default environment's UIs were reachable by name at all, because only its
// vhost was ever written.
//
// The default environment keeps the short form so that every URL already
// written down keeps meaning what it means. A wildcard still covers all of it:
// the resolver matches by suffix at any depth, a macOS resolver file captures
// the whole subtree, and nginx prefers the longest wildcard -- so
// *.dev2.ns.local beats *.ns.local for a name under dev2, deterministically.
//
// hmd-cli-helm renders something else entirely (HelmIngressDomain, with the
// literal "local" in every environment, via --set, which beats any values
// file). That is not changed here; the deployed Ingress object's host is
// rewritten instead, beside the path rewrite that already exists for exactly
// this class of local-versus-cloud mismatch (NERD025 SPEC005).
func IngressHostFor(instanceName, slug string) string {
	if slug == "" || slug == HelmLocalSlug {
		return instanceName + "." + IngressDomain
	}
	return instanceName + "." + slug + "." + IngressDomain
}

// EnvWildcardFor is the server_name an environment's vhost claims.
func EnvWildcardFor(slug string) string {
	if slug == "" || slug == HelmLocalSlug {
		return "*." + IngressDomain
	}
	return "*." + slug + "." + IngressDomain
}

// vhostServer is a Host-routed server block proxying everything to the ingress
// controller.
//
// Host is forwarded verbatim because Traefik picks the Ingress rule from it --
// rewriting it would make every UI resolve to the same backend, or none. The
// path passes through unchanged for the same reason, which is why
// passthroughLocation is not reusable here: it strips its own prefix. The
// Upgrade/Connection pair is what lets Argo stream workflow logs.
func vhostServer(serverName, upstream string) string {
	return fmt.Sprintf(`server {
    listen 80;
    server_name %s;
    location / {
        proxy_pass http://%s;
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
}`, serverName, upstream)
}

// WriteEnvVhosts writes an environment's Host-routed vhost fragment.
//
// One wildcard block rather than one per app: a wildcard covers every UI the
// environment deploys without this package having to know which apps exist.
//
// The wildcard is *.local.<domain> -- HelmLocalSlug, not the environment's own
// slug. That is what the charts render (see IngressHostFor), and writing
// *.<slug>. instead produced a server block that matched nothing whatsoever in
// any environment not named `local`.
//
// It follows that every environment's charts render the *same* hostnames, so
// only one environment can own the wildcard: a second block with the same
// server_name is a conflict nginx resolves by preferring whichever fragment it
// happened to read first. The default environment takes it, and the others are
// reached on their ports, which are allocated per environment and cannot
// collide. Giving each environment its own hostname would mean rewriting the
// host on the deployed Ingress object, which is a change to what the charts
// asked for and is deliberately not made here.
func (r *Router) WriteEnvVhosts(env Env, upstream string) error {
	// Every environment owns a wildcard now, not only the default one. They no
	// longer collide, because the environment is a label in the name: nginx
	// prefers the longest wildcard, so *.dev2.<suffix> takes a name under dev2
	// and *.<suffix> takes the default's (NERD025 SPEC005).
	blocks := []string{wrap(env.Slug+":vhost", vhostServer(EnvWildcardFor(env.Slug), upstream))}
	return r.writeFragment(filepath.Join(r.VhostDir(), EnvFragmentName(env.Slug)), blocks,
		fmt.Sprintf("environment %q vhosts", env.Slug))
}

// StreamsPort reports whether an environment's stream fragment carries a
// listener on a host port.
//
// This is the honest record of what an environment routes, and it is why the
// environment summary and `env status` can stop advertising Trino from the port
// slot. EnvStreamEntries emits a Trino listener if and only if startCluster
// found a coordinator, so the fragment answers "is Trino there" without a
// cluster call -- which `status` needs, because it must stay cheap and must
// answer on a stopped environment.
//
// A missing or unreadable fragment is false, not an error: an environment that
// has never started routes nothing, which is the same answer.
func (r *Router) StreamsPort(slug string, port int) bool {
	data, err := os.ReadFile(filepath.Join(r.StreamDir(), EnvFragmentName(slug)))
	if err != nil {
		return false
	}
	begin, _ := markers(slug + ":" + strconv.Itoa(port))
	return strings.Contains(string(data), begin)
}

// RoutesService reports whether an environment's HTTP fragment carries a route
// for one service.
//
// StreamsPort's reasoning, applied to the http.d fragment: the fragment is the
// record of what was actually routed, so it answers "is that service there"
// without asking Floci, cheaply, and on a stopped environment. Which matters
// now that a substrate service is deployed for a consumer rather than for a
// mode (NERD024 SPEC001) -- the mode no longer tells anyone whether the route
// exists.
//
// A missing or unreadable fragment is false, for StreamsPort's reason: an
// environment that has never started routes nothing.
func (r *Router) RoutesService(slug, service string) bool {
	data, err := os.ReadFile(filepath.Join(r.HTTPDir(), EnvFragmentName(slug)))
	if err != nil {
		return false
	}
	begin, _ := markers(slug + "/" + service)
	return strings.Contains(string(data), begin)
}

// RemoveEnvRoutes deletes every fragment an environment owns.
func (r *Router) RemoveEnvRoutes(slug string) error {
	name := EnvFragmentName(slug)
	var failed []string
	for _, dir := range []string{r.HTTPDir(), r.StreamDir(), r.VhostDir()} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			failed = append(failed, err.Error())
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("removing the fragments for %q: %s", slug, strings.Join(failed, "; "))
	}
	return nil
}

// UpsertServiceRoute splices one API Gateway route into its fragment without
// reloading.
//
// The bulk writers only see the services this CLI deploys itself. A service
// deployed through the DAG gets a CDKTF-managed gateway they know nothing
// about, so its route is spliced in separately, bounded by its own marker
// comments -- re-running after a redeploy replaces exactly that route.
func (r *Router) UpsertServiceRoute(env Env, routePath, restAPIID, stageName string) error {
	routePath = strings.Trim(routePath, "/")
	route := env.Slug + "/" + routePath
	fragment := filepath.Join(r.HTTPDir(), EnvFragmentName(env.Slug))
	block := wrap(route, r.apiLocation(route, FlociUpstreamHost, restAPIID, stageName, env.AccountID))
	return upsertBlock(fragment, route, block)
}

func upsertBlock(fragment, route, block string) error {
	begin, end := markers(route)
	existing, err := os.ReadFile(fragment)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", fragment, err)
	}
	text := string(existing)

	pattern, err := regexp.Compile("(?s)" + regexp.QuoteMeta(begin) + ".*?" + regexp.QuoteMeta(end) + "\n?")
	if err != nil {
		return fmt.Errorf("building the route pattern: %w", err)
	}
	if pattern.MatchString(text) {
		text = pattern.ReplaceAllLiteralString(text, block)
	} else {
		if strings.TrimSpace(text) != "" {
			text = strings.TrimRight(text, "\n") + "\n"
		} else {
			text = ""
		}
		text += block
	}
	if err := os.MkdirAll(filepath.Dir(fragment), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(fragment), err)
	}
	return writeFile(fragment, text)
}
