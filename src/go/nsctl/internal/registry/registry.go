// Package registry reads and writes hmd's environment registry:
// $HMD_HOME/.cache/neuronsphere/environments.json.
//
// The Python module that owns this file states the rule in its docstring and
// SPEC003 repeats it: every derived value is computed once at create time and
// then PERSISTED, so changing a derivation rule later can never orphan
// containers or volumes named under the old rule. nsctl therefore READS this
// file rather than re-deriving any of it.
//
// That is not merely tidy, it is correct for a case that exists in the field.
// An environment migrated from the pre-multi-env layout carries
// legacy_layout: true, shares the control plane's Postgres and graph rather
// than running its own, and so has a container named `hmd_db` -- matching no
// current naming rule at all.
//
// Struct fields are declared in ALPHABETICAL order on purpose. The Python
// writes with json.dumps(..., indent=2, sort_keys=True), and Go's encoder emits
// struct fields in declaration order, so alphabetical declaration is what makes
// the two produce the same bytes. A round-trip test would pass however wrong
// this was; the fixture is the literal JSON the Python CLI emits.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
)

// Version is the registry schema version the Python writes.
const Version = 1

// DefaultEnvName is the environment `up` uses when none is named.
const DefaultEnvName = "local"

// Port layout. hmd_proxy publishes the whole band up front -- a compose
// `ports:` list is static -- and individual nginx stream listeners inside it
// are added and removed with a reload, no restart.
const (
	// PortsPerEnv is the slot stride: Floci, Trino, graph, spare.
	PortsPerEnv = 4
	// DefaultPortBase is the first slot port.
	DefaultPortBase = 19000
	// MaxEnvs is both the slot count and the width of the k3s band above them.
	MaxEnvs = 16
	// MaxUIPorts is how many Ingress-exposed user interfaces can be published
	// on a host port at once, across every environment together.
	//
	// A band of its own above the k3s band, because the slot stride cannot be
	// widened -- see FlociPort. Shared rather than per-slot, and this is a cost
	// decision rather than a taste one: hmd_proxy publishes its whole band up
	// front, every port in it becomes an individual Engine API binding
	// (compose.portSpec) and an individual in-use probe (compose.HostPorts), and
	// the band is already eighty. A per-environment block of eight would make it
	// 216 to buy capacity for sixteen simultaneous environments nobody runs.
	// Thirty-two shared ports cover the environments people actually have open
	// and cost the start path thirty-two more bindings, not a hundred and
	// thirty-six.
	MaxUIPorts = 32
)

// The single Floci. Every environment is an emulated AWS *account* inside it,
// told apart by account_id alone, so these are derived constants rather than
// registry fields. A registry written by an older CLI still carries
// per-environment values for them; they are dropped on load.
const (
	FlociContainer = "floci"
	FlociAlias     = "neuronsphere"
)

// ControlPlaneAccountID is Floci's default account, reserved for the control
// plane.
const ControlPlaneAccountID = container.ControlPlaneAccountID

// slugPattern is env_registry._SLUG_RE. The name becomes a URL path segment
// (/<slug>/<service>/), a container-name suffix and a k3s cluster-name
// component, so it is kept to lowercase alphanumerics and hyphens.
const slugPattern = `^[a-z0-9][a-z0-9-]{0,15}$`

// reservedSlugs are route paths the control plane owns. An env slug may not
// shadow one, or /<slug>/ would swallow a control-plane route.
var reservedSlugs = map[string]bool{
	"api": true, "apiop": true, "argo": true, "aws": true, "restapis": true,
	"hmd_ms_deployment": true, "hmd_ms_naming": true, "hmd_ms_artifact_lib": true,
	"hmd_ms_dbaccount": true,
	"ms-deployment":    true, "ms_deployment": true, "hmd-ms-deployment": true,
	"ms-naming": true, "ms_naming": true, "hmd-ms-naming": true,
}

// Environment is one named local environment: a self-contained emulated AWS
// account with its own k3s cluster, Postgres and hmd-ms-dbaccount.
//
// Fields alphabetical -- see the package doc.
type Environment struct {
	AccountID string `json:"account_id"`
	// Bootstrap records {csd_nid, k3s_uid, mode} once the environment has been
	// bootstrapped. Kept as a raw map so a key hmd adds tomorrow survives a
	// round trip through nsctl.
	//
	// No omitempty: the Python dataclass defaults it to {} and asdict emits
	// `"bootstrap": {}` for an environment that has never bootstrapped, so
	// omitting the key -- or writing Go's nil-map `null` -- would diverge on
	// the very first `env add`. Save normalises nil to an empty map.
	Bootstrap        map[string]any `json:"bootstrap"`
	ComposeProject   string         `json:"compose_project"`
	CoreInstanceName string         `json:"core_instance_name"`
	DBContainer      string         `json:"db_container"`
	DeploymentID     string         `json:"deployment_id"`
	GraphContainer   string         `json:"graph_container"`
	K3sCluster       string         `json:"k3s_cluster"`
	Kubeconfig       string         `json:"kubeconfig"`
	// LegacyLayout marks an environment migrated from the pre-multi-env
	// install, where the control plane's Floci, Postgres and graph *are* the
	// default environment's. Such an environment has no compose project of its
	// own and its containers match no current naming rule.
	LegacyLayout bool   `json:"legacy_layout"`
	Name         string `json:"name"`
	PortBase     int    `json:"port_base"`
	PortSlot     int    `json:"port_slot"`
	Slug         string `json:"slug"`
	StateDir     string `json:"state_dir"`
	// UIPorts maps an Ingress hostname to the host port hmd_proxy serves it on.
	//
	// Persisted rather than recomputed from the current set of Ingress hosts,
	// for the reason this package's doc gives: a derived value that changes
	// later orphans what was named under the old rule. Here the thing named is
	// a URL a user has bookmarked, so a UI that moves ports because an
	// unrelated one was deployed first is a defect.
	//
	// omitempty: an environment that publishes no UI writes no key, so this
	// does not appear in a registry the Python CLI round-trips.
	UIPorts map[string]int `json:"ui_ports,omitempty"`
}

// Control-plane port names. These are the host ports hmd_proxy publishes, and
// the only ports a local NeuronSphere claims on the machine.
//
// Named rather than derived from a single base, because they are not one
// arithmetic family: 80 is a web port, 4566 is what Floci answers on, and the
// environment band is 112 contiguous ports. A base offset that fitted all three
// would be a fiction.
const (
	PortHTTP    = "http"
	PortFloci   = "floci"
	PortTrino   = "trino"
	PortDNS     = "dns"
	PortEnvBase = "env_base"
)

// defaultControlPlanePorts are the ports a home takes when they are free. They
// are the historical values, so an install that works today does not move.
var defaultControlPlanePorts = map[string]int{
	PortHTTP:    80,
	PortFloci:   4566,
	PortTrino:   18080,
	PortDNS:     19153,
	PortEnvBase: DefaultPortBase,
}

// ControlPlane is the shared half of a local NeuronSphere: one per HMD_HOME.
type ControlPlane struct {
	Bootstrapped   bool   `json:"bootstrapped"`
	ComposeProject string `json:"compose_project"`
	FlociDataDir   string `json:"floci_data_dir"`
	Network        string `json:"network"`
	// Ports are the host ports this home actually publishes, recorded the first
	// time they are chosen.
	//
	// Persisted rather than recomputed for the reason this package's doc gives,
	// and with a sharper edge than most: these appear in URLs people bookmark,
	// in a kubeconfig, and in an nginx fragment. A value that drifted would
	// point every one of them somewhere else.
	//
	// omitempty: a home that took the defaults writes no key, so a registry the
	// Python CLI round-trips is unchanged.
	Ports map[string]int `json:"ports,omitempty"`
}

// Port is the host port this home publishes for a named service, or the
// default where nothing was recorded. An unknown name answers 0, which is not a
// port -- a caller asking for one has a bug rather than a fallback.
func (c ControlPlane) Port(name string) int {
	if port, ok := c.Ports[name]; ok && port > 0 {
		return port
	}
	return defaultControlPlanePorts[name]
}

// EnvPortWidth is how many contiguous host ports an environment band needs: the
// per-environment slots, the k3s band above them, and the shared UI band.
const EnvPortWidth = MaxEnvs*(PortsPerEnv+1) + MaxUIPorts

// EnvPortRange is the contiguous band hmd_proxy publishes for environments,
// derived from whichever base this home took.
func (c ControlPlane) EnvPortRange() (int, int) {
	base := c.Port(PortEnvBase)
	return base, base + EnvPortWidth - 1
}

// Registry is the whole file.
type Registry struct {
	ControlPlane ControlPlane           `json:"control_plane"`
	DefaultEnv   string                 `json:"default_env"`
	Environments map[string]Environment `json:"environments"`
	Version      int                    `json:"version"`

	// Path is the file this was read from, for an error that can name it.
	Path string `json:"-"`
	// Synthesized is true when there was no registry file and this view was
	// derived from a pre-multi-env install. Nothing has been persisted; a
	// caller that mutates must Save.
	Synthesized bool `json:"-"`
}

// Lookup resolves an environment variable, returning "" when unset. An alias,
// so hmdenv.Lookup is the same type -- see its doc.
type Lookup = func(string) string

// NotBootstrappedError says an HMD_HOME has no local NeuronSphere at all, as
// distinct from one whose environment is merely stopped. The two need different
// next moves and a bare "not found" cannot tell them apart.
type NotBootstrappedError struct {
	Home string
	Path string
}

func (e *NotBootstrappedError) Error() string {
	return fmt.Sprintf("%s has no local NeuronSphere: %s does not exist. Create one with `nsctl control-plane start --home %s`",
		e.Home, e.Path, e.Home)
}

// UnknownEnvironmentError names what does exist, so a typo is one line from
// fixed. An allowlist that says only "no" makes the operator go looking for the
// list.
type UnknownEnvironmentError struct {
	Name      string
	Available []string
}

func (e *UnknownEnvironmentError) Error() string {
	if len(e.Available) == 0 {
		return fmt.Sprintf("no environment %q, and none are registered. Create one with `nsctl env add %s`", e.Name, e.Name)
	}
	return fmt.Sprintf("no environment %q. Registered: %s. Create it with `nsctl env add %s`",
		e.Name, strings.Join(e.Available, ", "), e.Name)
}

// Path is where hmd keeps the registry for a given HMD_HOME.
func Path(home string) string {
	return filepath.Join(home, ".cache", "neuronsphere", "environments.json")
}

// LegacyMarkerPath is the pre-multi-env bootstrap marker, read once during
// migration.
func LegacyMarkerPath(home string) string {
	return filepath.Join(home, ".cache", "neuronsphere", "bootstrap.json")
}

// EnvironmentsRoot is where per-environment state lives.
func EnvironmentsRoot(home string) string {
	return filepath.Join(home, ".cache", "environments")
}

// Load reads the registry for one HMD_HOME.
//
// When the file is absent it reproduces env_registry.load's migration: an
// install that a pre-multi-env CLI already bootstrapped is presented as a
// single legacy-layout `local` environment. The result is marked Synthesized
// and nothing is written -- a read-only command must not create state.
func Load(home string, lookup Lookup) (*Registry, error) {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	path := Path(home)

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return synthesize(home, lookup)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	r.Path = path
	r.applyDefaults(home, lookup)
	return &r, nil
}

// applyDefaults fills in what an older registry may omit. Reproduced rather
// than left zero: an empty network name would make every "is it up" probe
// answer no for a platform that is plainly running.
func (r *Registry) applyDefaults(home string, lookup Lookup) {
	if r.Environments == nil {
		r.Environments = map[string]Environment{}
	}
	if r.ControlPlane.Network == "" {
		r.ControlPlane.Network = firstNonEmpty(lookup("HMD_LOCAL_DOCKER_NETWORK"), container.DefaultNetwork(home))
	}
	if r.ControlPlane.ComposeProject == "" {
		r.ControlPlane.ComposeProject = firstNonEmpty(lookup("HMD_LOCAL_COMPOSE_PROJECT_NAME"), container.DefaultProject(home))
	}
	if r.ControlPlane.FlociDataDir == "" {
		r.ControlPlane.FlociDataDir = filepath.Join(home, "floci", "data")
	}
	if r.DefaultEnv == "" {
		r.DefaultEnv = DefaultEnvName
	}
	if r.Version == 0 {
		r.Version = Version
	}
	for slug, e := range r.Environments {
		if e.Slug == "" {
			e.Slug = slug
		}
		if e.Name == "" {
			e.Name = slug
		}
		if e.PortBase == 0 {
			e.PortBase = DefaultPortBase
		}
		r.Environments[slug] = e
	}
}

// synthesize builds the view for an HMD_HOME with no registry file.
func synthesize(home string, lookup Lookup) (*Registry, error) {
	r := &Registry{
		ControlPlane: ControlPlane{
			ComposeProject: firstNonEmpty(lookup("HMD_LOCAL_COMPOSE_PROJECT_NAME"), container.DefaultProject(home)),
			Network:        firstNonEmpty(lookup("HMD_LOCAL_DOCKER_NETWORK"), container.DefaultNetwork(home)),
			FlociDataDir:   filepath.Join(home, "floci", "data"),
		},
		DefaultEnv:   DefaultEnvName,
		Environments: map[string]Environment{},
		Version:      Version,
		Path:         Path(home),
		Synthesized:  true,
	}

	marker, migrate := legacyMarker(home)
	if !migrate {
		return r, nil
	}
	r.Environments[DefaultEnvName] = legacyEnvironment(home, lookup, marker)
	r.ControlPlane.Bootstrapped = true
	return r, nil
}

// legacyMarker reports whether this HMD_HOME was already bootstrapped by a
// pre-multi-env CLI, and returns the marker's contents when it can read them.
func legacyMarker(home string) (map[string]any, bool) {
	if data, err := os.ReadFile(LegacyMarkerPath(home)); err == nil {
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return map[string]any{}, true
		}
		return m, true
	}
	// Floci state used to be taken as evidence that a prior `up` ran, on the
	// grounds that only a completed bootstrap could have produced it. That is
	// no longer true: nsctl's own bootstrap starts Floci as its first step, so
	// a bootstrap that fails partway leaves exactly this state behind. Reading
	// it as "already bootstrapped" made the retry skip the bootstrap and
	// report a control plane ready that had no database and no services.
	//
	// The two cases cannot be told apart from the directory alone, so the
	// tie-break is which mistake is worse. Bootstrapping an already-
	// bootstrapped home is idempotent and costs a few minutes; skipping it on
	// a home that needs it produces a control plane that claims to be ready
	// and is not. So an unmarked home is treated as never bootstrapped, and a
	// genuine legacy install is recognised by its marker.
	return nil, false
}

// legacyEnvironment synthesizes the default env for an install that predates
// the registry. In that layout the control plane's Floci, Postgres and
// JanusGraph *are* the default environment's, and the k3s cluster keeps the
// name Floci already spawned its container and volume under. Nothing moves.
func legacyEnvironment(home string, lookup Lookup, marker map[string]any) Environment {
	if marker == nil {
		marker = map[string]any{}
	}
	return Environment{
		AccountID:        ControlPlaneAccountID,
		Bootstrap:        marker,
		ComposeProject:   firstNonEmpty(lookup("HMD_LOCAL_COMPOSE_PROJECT_NAME"), container.DefaultProject(home)),
		CoreInstanceName: "local-neuronsphere",
		DBContainer:      "hmd_db",
		DeploymentID:     DefaultEnvName,
		GraphContainer:   "global-graph",
		K3sCluster:       firstNonEmpty(lookup("HMD_LOCAL_K3S_CLUSTER_NAME"), legacyClusterName(home)),
		Kubeconfig:       firstNonEmpty(lookup("HMD_LOCAL_K3S_KUBECONFIG"), filepath.Join(home, ".cache", "k3s", "kubeconfig")),
		LegacyLayout:     true,
		Name:             DefaultEnvName,
		PortBase:         portBase(lookup),
		PortSlot:         0,
		Slug:             DefaultEnvName,
		StateDir:         home,
	}
}

func legacyClusterName(home string) string {
	if h := container.HMDHomeHash(home); h != "" {
		return "neuronsphere-" + h
	}
	return "neuronsphere"
}

func portBase(lookup Lookup) int {
	if v := lookup("HMD_LOCAL_ENV_PORT_BASE"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return DefaultPortBase
}

// Save writes the registry atomically.
//
// `env add` and a concurrent `env start` can both write, and a torn registry
// would strand running containers with no record of their names -- so write to
// a temp file in the same directory and rename over the target.
//
// The encoding matches json.dumps(indent=2, sort_keys=True): two-space indent,
// keys in alphabetical order (struct fields are declared that way), and no
// trailing newline. HTML escaping is off because Python does not escape
// <, > or &. One residual difference: Python's ensure_ascii=True escapes
// non-ASCII as \uXXXX and Go emits it raw, which would only show up in a path
// containing non-ASCII.
func (r *Registry) Save(home string) error {
	path := Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}

	// A nil map marshals as `null`; the Python writes `{}`.
	for slug, e := range r.Environments {
		if e.Bootstrap == nil {
			e.Bootstrap = map[string]any{}
			r.Environments[slug] = e
		}
	}
	if r.Environments == nil {
		r.Environments = map[string]Environment{}
	}

	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("encoding the registry: %w", err)
	}
	// Encode appends a newline; json.dumps does not.
	payload := strings.TrimSuffix(buf.String(), "\n")

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(payload), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	r.Path = path
	r.Synthesized = false
	return nil
}

// Names lists the registered environment slugs, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.Environments))
	for k := range r.Environments {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Environment resolves one environment by explicit name, HMD_LOCAL_ENV, or the
// registry default.
//
// A missing environment is an error, never a creation: `env add` is an explicit
// action, not a side effect of a typo. What makes that the right rule is that
// something else is registered -- creating `dveelop` because `develop` was
// misspelt is worse than refusing. On a registry holding nothing at all there
// is no typo to protect against and nothing to be confused with, which is why
// EnsureFirstEnvironment exists and why it is the only exception.
func (r *Registry) Environment(name string, lookup Lookup) (*Environment, error) {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	requested := name
	if requested == "" {
		requested = lookup("HMD_LOCAL_ENV")
	}
	if requested == "" {
		requested = r.DefaultEnv
	}
	e, ok := r.Environments[slugify(requested)]
	if !ok {
		return nil, &UnknownEnvironmentError{Name: requested, Available: r.Names()}
	}
	return &e, nil
}

// UsedSlots reports the port slots already allocated.
func (r *Registry) UsedSlots() map[int]bool {
	used := map[int]bool{}
	for _, e := range r.Environments {
		used[e.PortSlot] = true
	}
	return used
}

// UsedAccounts reports the account ids already allocated, including the control
// plane's.
func (r *Registry) UsedAccounts() map[string]bool {
	used := map[string]bool{ControlPlaneAccountID: true}
	for _, e := range r.Environments {
		used[e.AccountID] = true
	}
	return used
}

// AllocatePortSlot picks a stable port slot for a name.
//
// Hashing the name first means the same environment name usually lands on the
// same ports across machines and re-creations, which keeps bookmarks and
// scripts stable; the lowest-free fallback keeps allocation correct when two
// names collide.
func (r *Registry) AllocatePortSlot(name string) (int, error) {
	used := r.UsedSlots()
	if len(used) >= MaxEnvs {
		return 0, fmt.Errorf("all %d environment port slots are in use; delete one with `nsctl env delete <name>` first", MaxEnvs)
	}
	preferred := int(crc32.ChecksumIEEE([]byte(name))) % MaxEnvs
	if !used[preferred] {
		return preferred, nil
	}
	for slot := 0; slot < MaxEnvs; slot++ {
		if !used[slot] {
			return slot, nil
		}
	}
	return 0, fmt.Errorf("no free environment port slot")
}

// AllocateAccountID returns the next unused 12-digit emulated AWS account id.
func (r *Registry) AllocateAccountID() string {
	used := r.UsedAccounts()
	for n := 1; ; n++ {
		id := fmt.Sprintf("%012d", n)
		if !used[id] {
			return id
		}
	}
}

// -- derived values -------------------------------------------------------

// FlociPort is the slot's base port.
//
// It no longer carries a Floci stream listener -- the single Floci is reached
// on the control plane's :4566 -- but the slot layout is deliberately
// unchanged: TrinoPort, GraphPort and SparePort are offsets from it, and slot
// 0's spare is the Deployment GUI's published 19003. Renumbering to reclaim one
// port would move every environment's Trino and the GUI.
func (e *Environment) FlociPort() int { return e.base() + e.PortSlot*PortsPerEnv }

// TrinoPort is where hmd_proxy streams this environment's Trino coordinator.
func (e *Environment) TrinoPort() int { return e.FlociPort() + 1 }

// GraphPort is the slot's graph listener.
func (e *Environment) GraphPort() int { return e.FlociPort() + 2 }

// SparePort is reserved for the next port-routed UI, so adding one needs no
// renumbering.
func (e *Environment) SparePort() int { return e.FlociPort() + 3 }

// K3sPort is the host port hmd_proxy streams to this environment's k3s API.
//
// A band above the slot ports, not a fifth slot port: widening the stride would
// renumber every existing environment's Trino, graph and spare, and slot 0's
// spare is the Deployment GUI's published 19003.
//
// The API is reached through hmd_proxy at all because Docker re-creates the
// port forward on the floci-eks-* container every restart, and a re-created
// forward silently truncates writes past roughly one MTU -- which a TLS 1.3
// ClientHello carrying a post-quantum key share (1449 bytes, what kubectl and
// OpenSSL >= 3.5 send by default) exceeds. Every client then hangs with
// "net/http: TLS handshake timeout" against a perfectly healthy cluster.
func (e *Environment) K3sPort() int { return e.base() + MaxEnvs*PortsPerEnv + e.PortSlot }

// UIPortAt is the idx'th port in the shared UI band.
//
// A third band, above the k3s one, for the same reason K3sPort is a band rather
// than a fifth slot port: widening PortsPerEnv would renumber every existing
// environment's Trino, graph and spare, and slot 0's spare is the Deployment
// GUI's published 19003.
//
// It does not depend on the port slot: the band is shared across environments,
// so the receiver contributes only its port base.
func (e *Environment) UIPortAt(idx int) int {
	return e.base() + MaxEnvs*PortsPerEnv + MaxEnvs + idx
}

// UIPort reports the port already assigned to an Ingress hostname.
//
// It never allocates. A caller that wants a port for a host it has just
// discovered wants AssignUIPort; this answers for the summary and for
// `env status`, where inventing a port would advertise one nothing listens on.
func (e *Environment) UIPort(host string) (int, bool) {
	port, ok := e.UIPorts[host]
	return port, ok
}

// UsedUIPorts is every host port already promised to a user interface, in any
// environment. The band is shared, so allocation has to see all of them.
func (r *Registry) UsedUIPorts() map[int]string {
	used := map[int]string{}
	for _, e := range r.Environments {
		for host, port := range e.UIPorts {
			used[port] = host
		}
	}
	return used
}

// AssignUIPort returns the host port an Ingress hostname is served on,
// allocating and recording one the first time it is asked.
//
// Hashing the hostname first means a UI usually lands on the same port across
// machines and re-creations even before anything is persisted -- the same
// reasoning as AllocatePortSlot -- and the lowest-free fallback keeps it
// correct when two hostnames collide. The recorded map then pins the choice, so
// a later collision cannot move a port that is already in use, which matters
// because the port is an address someone has bookmarked.
//
// Allocation consults every environment, not just this one: two environments
// deploying the same chart produce the same hostname today (see
// router.IngressHostFor) and must still not be handed the same port.
//
// The caller persists. This mutates env, and an Environment lives in the
// registry by value.
func (r *Registry) AssignUIPort(env *Environment, host string) (int, error) {
	if port, ok := env.UIPorts[host]; ok {
		return port, nil
	}
	taken := r.UsedUIPorts()
	for _, port := range env.UIPorts {
		taken[port] = host
	}
	if len(taken) >= MaxUIPorts {
		return 0, fmt.Errorf(
			"all %d published user-interface ports are in use, so %s has no host port; reach it by hostname, or free one by deleting an environment that no longer needs its UIs",
			MaxUIPorts, host)
	}
	idx := int(crc32.ChecksumIEEE([]byte(host))) % MaxUIPorts
	if _, clash := taken[env.UIPortAt(idx)]; clash {
		for i := 0; i < MaxUIPorts; i++ {
			if _, clash := taken[env.UIPortAt(i)]; !clash {
				idx = i
				break
			}
		}
	}
	port := env.UIPortAt(idx)
	if env.UIPorts == nil {
		env.UIPorts = map[string]int{}
	}
	env.UIPorts[host] = port
	return port, nil
}

func (e *Environment) base() int {
	if e.PortBase == 0 {
		return DefaultPortBase
	}
	return e.PortBase
}

// IsDefault reports whether this is the `local` environment, which also writes
// the historical shared kubeconfig path.
func (e *Environment) IsDefault() bool { return e.Slug == DefaultEnvName }

// Bootstrapped reports whether this environment has completed a bootstrap,
// which is what the reconcile fast path keys on.
func (e *Environment) Bootstrapped() bool {
	if e.Bootstrap == nil {
		return false
	}
	nid, ok := e.Bootstrap["csd_nid"].(string)
	return ok && nid != ""
}

// StatePath is the environment's state directory.
func (e *Environment) StatePath() string { return e.StateDir }

// PostgresDataDir, GraphDataDir and KubeconfigDir are the directories that must
// exist before the environment starts.
func (e *Environment) PostgresDataDir() string {
	return filepath.Join(e.StateDir, "postgresql", "data")
}

// GraphDataDir is the environment's graph state directory.
func (e *Environment) GraphDataDir() string { return filepath.Join(e.StateDir, "graph_db") }

// FlociDataDir is kept only so a pre-collapse install can be spotted: an
// environment's Floci state now lives inside the single control-plane Floci's
// data dir, namespaced by account.
func (e *Environment) FlociDataDir() string {
	return filepath.Join(e.StateDir, "floci", "data")
}

// StateDirs is every directory that must exist before the environment starts.
// FlociDataDir is deliberately absent -- see its doc.
func (e *Environment) StateDirs() []string {
	return []string{
		e.PostgresDataDir(),
		e.GraphDataDir(),
		filepath.Dir(e.Kubeconfig),
	}
}

// -- names ----------------------------------------------------------------

func slugify(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// ValidateSlug normalises and checks an environment name.
func ValidateSlug(name string) (string, error) {
	slug := slugify(name)
	if slug == "" {
		return "", errors.New("environment name must not be empty")
	}
	if !matchSlug(slug) {
		return "", fmt.Errorf("invalid environment name %q: use 1-16 characters, lowercase letters, digits and hyphens, starting with a letter or digit", name)
	}
	if reservedSlugs[slug] {
		return "", fmt.Errorf("environment name %q is reserved -- it would shadow a control-plane route at http://localhost/%s/", slug, slug)
	}
	return slug, nil
}

// matchSlug implements slugPattern without a regexp: 1-16 characters, the first
// alphanumeric and the rest alphanumeric or hyphen.
func matchSlug(s string) bool {
	if len(s) < 1 || len(s) > 16 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
