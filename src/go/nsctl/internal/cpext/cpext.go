// Package cpext resolves the control-plane extensions a user declares in
// $HMD_HOME/.config/control-plane.yaml, per NERD004.
//
// An extension is a RepoClass that runs as containers beside the control
// plane's own, from a compose file the RepoClass carries at
// src/local/docker-compose.extension.yml. It is not a deployment: it is not
// seeded into a BOM, never reaches hmd-ms-deployment, and appears in no
// ChangeSet. That is what lets it be up before the deployment graph exists,
// which is the property a package registry needs -- `hmd build` runs long
// before anything has been deployed.
//
// Nothing here scans or enumerates. An extension is resolved because the
// manifest names it, and for no other reason (SPEC013).
package cpext

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// ComposeFile is where a RepoClass declares the containers it contributes.
//
// A fixed name, in the src/local directory that already holds a repo's
// local-deploy alternates. Named .extension.yml rather than
// .control-plane.yml so it is never confused with nsctl's own bundled
// services/docker-compose.control-plane.yml, which describes the control
// plane rather than an addition to it.
const ComposeFile = "docker-compose.extension.yml"

// PlatformNetwork is the network key the bundled control-plane compose file
// uses, and the one an extension's services attach to.
//
// Duplicated from that file rather than read out of it, because an extension
// is parsed before the core project is available in every caller.
// TestBundledProjectDeclaresThePlatformNetwork in internal/controlplane fails
// if the two ever drift, so the duplication is checked rather than trusted.
const PlatformNetwork = "neuronsphere_default"

// ConfigPrefix and the NS_EXTENSION_* names are nsctl's, which is why SPEC005
// resolves them ahead of the process environment: the manifest is the
// declarative source of truth for them, and a stray export silently beating a
// checked-in manifest is a bug with no symptom.
const ConfigPrefix = "NS_CONFIG_"

// Names of the four facts nsctl knows and a manifest should not have to repeat.
const (
	VarName     = "NS_EXTENSION_NAME"
	VarVersion  = "NS_EXTENSION_VERSION"
	VarStateDir = "NS_EXTENSION_DIR"
	VarRepoDir  = "NS_EXTENSION_REPO_DIR"
)

// Configuration keys with a meaning to nsctl rather than to the compose file.
const (
	KeyURL      = "url"
	KeyUpstream = "upstream"
	KeyProfiles = "profiles"
)

// instanceNamePattern keeps an instance name usable as a compose service key
// and therefore as part of a container name. manifest.Validate does not check
// this, because an environment instance name never becomes one.
var instanceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Extension is one declared extension, resolved as far as it got.
//
// An extension that failed to resolve is still an Extension, carrying its Err.
// Dropping it would lose the only place its name appears, and `status` has to
// be able to say "declared, and here is why it is not running" (SPEC006).
type Extension struct {
	Instance  string
	RepoClass string
	Version   string
	// Source is where the version came from -- a pin, a working tree, a
	// bundled tree. SPEC012 puts it in `status` because "0.1.4 from a working
	// tree" and "0.1.4 from an artifact" are different facts.
	Source   repoclass.Source
	RepoDir  string
	StateDir string
	// URL is the single address this extension serves at, and Host the
	// hostname derived from it. Empty when it serves nothing.
	URL  string
	Host string
	// Upstream is "<container>:<port>" for the nginx vhost, resolved from the
	// declared "<service key>:<port>".
	Upstream string
	Config   map[string]any
	// Credentials are the keychain references this extension declares
	// (NERD004 SPEC009). Each carries whether it resolved and, if not, why --
	// never the value.
	Credentials []Credential
	// Handback is the environment variables this extension contributes back
	// into hmd.env (NERD004 SPEC010). Never a credential: SPEC009 keeps those
	// off disk, and handbackFrom refuses a reference to one.
	Handback []hmdenv.Var
	Services []compose.Service
	// Profiles is the active profile set this extension's configuration
	// selects, already namespaced by instance name.
	Profiles map[string]bool
	// Err is why this extension did not resolve. Services is empty when set.
	Err error
}

// Failed reports that this extension could not be resolved.
func (e Extension) Failed() bool { return e.Err != nil }

// Options is what resolving needs. Lookup is the control plane's own layered
// compose lookup, which the extension's is layered on top of.
type Options struct {
	Home        string
	RepoHome    string
	ProjectName string
	Lookup      compose.Lookup
	// Networks is the control plane's network map, so an extension's service
	// resolves the same real network name the core services do.
	Networks map[string]compose.Network
}

// Resolve reads the manifest and resolves every extension it declares.
//
// The error is reserved for a manifest that is itself unreadable or invalid.
// A single extension that cannot be resolved is recorded in its own Err and
// does not affect the others -- SPEC006's rule, applied one stage earlier than
// the container start.
//
// No manifest is not an error: it returns nothing, and the control plane
// starts exactly as it does today.
func Resolve(opts Options) ([]Extension, error) {
	m, err := manifest.LoadControlPlane(opts.Home, lookupOrEmpty(opts.Lookup))
	if err != nil || m == nil {
		return nil, err
	}
	resolver := repoclass.NewWithHome(opts.RepoHome, opts.Home, lookupOrEmpty(opts.Lookup))
	// The same seeding an environment apply does, and deliberately the same
	// code: declared trees into Paths, declared artifacts into Artifacts, and
	// never the $HMD_REPO_HOME/<class> convention into either.
	repoclass.Seed(resolver, m.Repos)

	exts := make([]Extension, 0, len(m.Repos))
	for _, r := range m.Repos {
		exts = append(exts, resolveOne(r, resolver, opts))
	}
	return exts, nil
}

// Manifest is the control-plane manifest, or nil when there is none. Exposed
// so `status` can report where a declaration came from without resolving it.
func Manifest(home string, lookup compose.Lookup) (*manifest.Manifest, error) {
	return manifest.LoadControlPlane(home, lookupOrEmpty(lookup))
}

func resolveOne(r manifest.Repo, resolver *repoclass.Resolver, opts Options) Extension {
	e := Extension{
		Instance:  r.InstanceName,
		RepoClass: r.RepoClassName,
		Config:    r.InstanceConfiguration,
		StateDir:  StateDir(opts.Home, r.InstanceName),
	}
	resolution := resolver.ResolveVersion(r.RepoClassName, r.Version)
	e.Version, e.Source = resolution.Version, resolution.Source

	if !instanceNamePattern.MatchString(r.InstanceName) {
		e.Err = fmt.Errorf("%q is not a usable instance name: it becomes part of a container name, "+
			"so it must start with a letter or digit and hold only lowercase letters, digits, dot, dash or underscore",
			r.InstanceName)
		return e
	}

	e.RepoDir = resolver.Dir(r.RepoClassName)
	if e.RepoDir == "" {
		if r.SourceType() == manifest.SourceArtifact {
			// A different failure with a different fix, so it gets a different
			// message: the manifest asked for a version nothing has fetched, and
			// telling the reader there is no checkout sends them to clone a repo
			// they deliberately chose not to.
			spec := librarian.Spec{Name: r.RepoClassName, Version: r.Version, ItemType: r.ArtifactType()}
			e.Err = fmt.Errorf("%w\nFetch it with:  nsctl artifact pull %s",
				artifact.Unavailable(opts.Home, r.RepoClassName, r.Version, spec.ContentPath(), opts.RepoHome, nil),
				spec.String())
			return e
		}
		e.Err = fmt.Errorf("no working tree for %s: nsctl carries none, the manifest declares no source.path, "+
			"and there is none under $HMD_REPO_HOME (%s)", r.RepoClassName, orNotSet(opts.RepoHome))
		return e
	}
	// A declared source.path is taken on the manifest's word rather than
	// stat-ed by the resolver, so a path with a typo in it arrives here.
	if info, err := os.Stat(e.RepoDir); err != nil || !info.IsDir() {
		e.Err = fmt.Errorf("no working tree for %s: %s is not a directory", r.RepoClassName, e.RepoDir)
		return e
	}

	data, err := readComposeFile(e.RepoDir)
	if err != nil {
		e.Err = err
		return e
	}

	if e.Credentials, err = credentialsFrom(r.InstanceConfiguration, Lookup(e, opts.Lookup)); err != nil {
		e.Err = err
		return e
	}
	if e.Handback, err = handbackFrom(r.InstanceName, r.InstanceConfiguration, Lookup(e, opts.Lookup)); err != nil {
		e.Err = err
		return e
	}
	e.Profiles = ActiveProfiles(r.InstanceName, r.InstanceConfiguration)
	parsed, err := compose.Parse(data, opts.ProjectName, Lookup(e, opts.Lookup))
	if err != nil {
		e.Err = fmt.Errorf("%s: %w", filepath.Join(e.RepoDir, "src", "local", ComposeFile), err)
		return e
	}

	services, err := adopt(r.InstanceName, parsed.Services, opts.Networks)
	if err != nil {
		e.Err = err
		return e
	}
	e.Services = services

	if err := e.resolveRoute(opts.ProjectName); err != nil {
		e.Err = err
		e.Services = nil
		return e
	}
	return e
}

// readComposeFile reads src/local/docker-compose.extension.yml.
//
// A src/local holding some other docker-compose file and not this one is named
// in the error: "no compose file" would be a lie the reader has to go and
// disprove, and the mistake is one rename away from being fixed.
func readComposeFile(repoDir string) ([]byte, error) {
	local := filepath.Join(repoDir, "src", "local")
	data, err := os.ReadFile(filepath.Join(local, ComposeFile))
	if err == nil {
		return data, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading %s: %w", filepath.Join(local, ComposeFile), err)
	}
	if others := otherComposeFiles(local); len(others) > 0 {
		return nil, fmt.Errorf("%s has %s but no %s; a control-plane extension's compose file must have that exact name",
			local, strings.Join(others, " and "), ComposeFile)
	}
	return nil, fmt.Errorf("%s has no %s, so this repo class contributes no containers",
		local, ComposeFile)
}

func otherComposeFiles(local string) []string {
	var found []string
	for _, pattern := range []string{"docker-compose*.yml", "docker-compose*.yaml"} {
		matches, _ := filepath.Glob(filepath.Join(local, pattern))
		for _, m := range matches {
			found = append(found, filepath.Base(m))
		}
	}
	sort.Strings(found)
	return found
}

// adopt applies SPEC004's rules and namespaces the services by instance.
func adopt(instance string, services []compose.Service, networks map[string]compose.Network) ([]compose.Service, error) {
	if len(services) == 0 {
		return nil, fmt.Errorf("%s declares no services", ComposeFile)
	}
	out := make([]compose.Service, 0, len(services))
	for _, s := range services {
		where := fmt.Sprintf("service %q", s.Key)
		if s.ContainerName != "" {
			return nil, fmt.Errorf("%s pins container_name %q. An extension may not: a pinned name is global, "+
				"and is how a second HMD_HOME's control plane recreates this one's containers. Remove it and the "+
				"container is named from the compose project, which already carries this HMD_HOME's digest",
				where, s.ContainerName)
		}
		if len(s.Ports) > 0 {
			return nil, fmt.Errorf("%s publishes a host port. Only hmd_proxy may; set instance_configuration.url "+
				"and instance_configuration.upstream instead, and nsctl serves it by name", where)
		}
		s.Key = instance + "-" + s.Key
		for i, p := range s.Profiles {
			s.Profiles[i] = instance + "-" + p
		}
		if len(s.Networks) == 0 {
			s.Networks = []compose.NetworkAttachment{{Name: PlatformNetwork}}
		}
		for _, attach := range s.Networks {
			if _, ok := networks[attach.Name]; !ok && networks != nil {
				return nil, fmt.Errorf("%s joins network %q, which the control plane does not declare; use %q",
					where, attach.Name, PlatformNetwork)
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// resolveRoute turns the declared url and upstream into a hostname and a
// container address (SPEC007).
//
// Both or neither: a URL nothing answers for produces a vhost that 502s, and
// an upstream with no URL produces a container nothing routes to. Either alone
// is a mistake worth naming at resolve time.
func (e *Extension) resolveRoute(projectName string) error {
	rawURL, _ := e.Config[KeyURL].(string)
	upstream, _ := e.Config[KeyUpstream].(string)
	switch {
	case rawURL == "" && upstream == "":
		return nil
	case rawURL == "":
		return fmt.Errorf("instance_configuration sets %q but no %q, so nothing routes to it", KeyUpstream, KeyURL)
	case upstream == "":
		return fmt.Errorf("instance_configuration sets %q but no %q, so nothing answers for it", KeyURL, KeyUpstream)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return fmt.Errorf("instance_configuration.%s is not a URL with a hostname: %q", KeyURL, rawURL)
	}
	key, port, ok := strings.Cut(upstream, ":")
	if !ok || key == "" || port == "" {
		return fmt.Errorf("instance_configuration.%s must be \"<service>:<port>\", got %q", KeyUpstream, upstream)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("instance_configuration.%s has a non-numeric port: %q", KeyUpstream, upstream)
	}

	for _, s := range e.Services {
		if s.Key == e.Instance+"-"+key {
			e.URL, e.Host = rawURL, parsed.Hostname()
			e.Upstream = s.Name(projectName) + ":" + port
			return nil
		}
	}
	return fmt.Errorf("instance_configuration.%s names service %q, which %s does not declare",
		KeyUpstream, key, ComposeFile)
}

// StateDir is where an extension keeps durable data (SPEC008): a sibling of
// floci/ and environments/, deliberately not under .cache/.
func StateDir(home, instance string) string { return filepath.Join(home, instance) }

// Lookup is the variable resolution an extension's compose file sees.
//
// NS_CONFIG_* and NS_EXTENSION_* first, then the control plane's own layered
// lookup -- process environment, then hmd.env. The inversion is SPEC005's and
// is deliberate: those names belong to nsctl, and the manifest is the
// declarative source of truth for them.
func Lookup(e Extension, base compose.Lookup) compose.Lookup {
	vars := ConfigVars(e)
	return func(key string) string {
		if v, ok := vars[key]; ok {
			return v
		}
		if base == nil {
			return ""
		}
		return base(key)
	}
}

// ConfigVars flattens an extension's configuration into the variables its
// compose file interpolates.
//
//	pypi: {enabled: true}   ->  NS_CONFIG_PYPI_ENABLED=true
//
// Nested maps recurse; lists are JSON-encoded whole, because an index-suffixed
// variable per element is a shape no compose file can consume.
func ConfigVars(e Extension) map[string]string {
	vars := map[string]string{
		VarName:     e.Instance,
		VarVersion:  e.Version,
		VarStateDir: e.StateDir,
		VarRepoDir:  e.RepoDir,
	}
	flatten(ConfigPrefix, e.Config, vars)
	return vars
}

func flatten(prefix string, config map[string]any, out map[string]string) {
	for key, value := range config {
		name := prefix + varName(key)
		switch v := value.(type) {
		case map[string]any:
			flatten(name+"_", v, out)
		case map[any]any:
			// yaml.v3 decodes into map[string]any, but a JSON manifest
			// round-tripped through it can still produce this shape.
			converted := make(map[string]any, len(v))
			for k, val := range v {
				converted[fmt.Sprint(k)] = val
			}
			flatten(name+"_", converted, out)
		case nil:
			out[name] = ""
		case string:
			out[name] = v
		case bool:
			out[name] = strconv.FormatBool(v)
		case int:
			out[name] = strconv.Itoa(v)
		case float64:
			out[name] = strconv.FormatFloat(v, 'f', -1, 64)
		default:
			if data, err := json.Marshal(v); err == nil {
				out[name] = string(data)
			}
		}
	}
}

// varName renders a configuration key as a variable name component.
func varName(key string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(key) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ActiveProfiles is the profile set an extension's configuration selects,
// namespaced by instance so two extensions using the same profile name do not
// switch each other on.
//
// Three spellings, all accepted (SPEC005). The `enabled: true` block is there
// because that is how NERD006 already writes it, and a mechanism that made its
// first consumer restate its configuration would be the wrong mechanism.
func ActiveProfiles(instance string, config map[string]any) map[string]bool {
	active := map[string]bool{}
	for key, value := range config {
		switch v := value.(type) {
		case bool:
			if v {
				active[instance+"-"+key] = true
			}
		case map[string]any:
			if enabled, ok := v["enabled"].(bool); ok && enabled {
				active[instance+"-"+key] = true
			}
		case []any:
			// Only the profiles key. `credentials` is a list too, and reading
			// it as profile names would switch components on by accident.
			if key != KeyProfiles {
				continue
			}
			for _, item := range v {
				if name, ok := item.(string); ok {
					active[instance+"-"+name] = true
				}
			}
		}
	}
	return active
}

// Project is the compose project an extension set runs as.
//
// It carries the *same* project name as the control plane's own, so the
// compose labels group them together and `docker compose -p <project> ps`
// still sees the whole platform. Extensions that failed to resolve contribute
// nothing.
func Project(exts []Extension, opts Options) *compose.Project {
	p := &compose.Project{Name: opts.ProjectName, Networks: opts.Networks}
	for _, e := range exts {
		if e.Failed() {
			continue
		}
		p.Services = append(p.Services, e.Services...)
	}
	sort.Slice(p.Services, func(i, j int) bool { return p.Services[i].Key < p.Services[j].Key })
	return p
}

// Profiles is the union of every resolved extension's active profiles.
func Profiles(exts []Extension) map[string]bool {
	active := map[string]bool{}
	for _, e := range exts {
		for name := range e.Profiles {
			active[name] = true
		}
	}
	return active
}

// Hosts are the hostnames extensions serve at, sorted. hmd_proxy takes each as
// a network alias so a sibling container resolves the same name the host does.
func Hosts(exts []Extension) []string {
	var hosts []string
	for _, e := range exts {
		if !e.Failed() && e.Host != "" {
			hosts = append(hosts, e.Host)
		}
	}
	sort.Strings(hosts)
	return hosts
}

func lookupOrEmpty(l compose.Lookup) func(string) string {
	if l == nil {
		return func(string) string { return "" }
	}
	return l
}

func orNotSet(v string) string {
	if v == "" {
		return "not set"
	}
	return v
}
