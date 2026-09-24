package compose

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Project is one compose file resolved for one project name.
type Project struct {
	// Name is the compose project. It becomes the com.docker.compose.project
	// label, which is how the Python CLI's `docker compose ps|stop|down` finds
	// containers nsctl created.
	Name string
	// Services in a deterministic order, so output and tests do not depend on
	// Go's map iteration.
	Services []Service
	Networks map[string]Network

	// WorkingDir and ConfigFile are stamped onto containers as the labels
	// compose writes, so `docker compose -p <project> ps` works from anywhere
	// rather than only alongside the file. Both are optional.
	WorkingDir string
	ConfigFile string
}

// Service is one container to run.
type Service struct {
	// Key is the service key in the YAML, which becomes
	// com.docker.compose.service.
	Key           string
	ContainerName string
	Image         string
	Restart       string
	Profiles      []string
	Command       []string
	Environment   map[string]string
	Ports         []Port
	Volumes       []Mount
	// Tmpfs are in-memory mounts. NERD004 SPEC009 delivers a resolved
	// credential into one of these: it is written after the container starts
	// and never touches the host filesystem, so nothing appears under
	// $HMD_HOME and nothing appears in `docker inspect`.
	Tmpfs       []string
	Networks    []NetworkAttachment
	Healthcheck *Healthcheck
}

// MountsTmpfs reports whether this service declares a tmpfs at path.
func (s Service) MountsTmpfs(path string) bool {
	for _, t := range s.Tmpfs {
		if t == path {
			return true
		}
	}
	return false
}

// Name is the container name to create, falling back to the compose default
// when the file does not pin one.
func (s Service) Name(project string) string {
	if s.ContainerName != "" {
		return s.ContainerName
	}
	return project + "-" + s.Key + "-1"
}

// Port is a published port or port range.
type Port struct {
	// HostIP is the interface the host side binds to, empty for all of them.
	// A service that must not be reachable from off the machine says so here;
	// the local resolver is the case that needed it.
	HostIP                       string
	HostStart, HostEnd           int
	ContainerStart, ContainerEnd int
	Protocol                     string
}

// Mount is a bind mount. Named volumes are not supported: nothing in the
// bundled file uses one, and silently treating a named volume as a host path
// would create a directory instead of failing.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// NetworkAttachment is one network the service joins, with any aliases.
type NetworkAttachment struct {
	Name    string
	Aliases []string
}

// Healthcheck is the container's health probe.
type Healthcheck struct {
	Test        []string
	Interval    time.Duration
	Timeout     time.Duration
	StartPeriod time.Duration
	Retries     int
}

// Network is a top-level network declaration.
type Network struct {
	// Name is the real Docker network name, which may differ from the key.
	Name     string
	External bool
}

// EnvKeys returns the environment variable names in sorted order.
func (s Service) EnvKeys() []string {
	keys := make([]string, 0, len(s.Environment))
	for k := range s.Environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// EnabledBy reports whether this service runs under the given active profiles.
// A service with no profiles always runs; one with profiles runs only when at
// least one of them is active. This is what
// HMD_LOCAL_NEURONSPHERE_ENABLE_GUI=false turns off.
func (s Service) EnabledBy(active map[string]bool) bool {
	if len(s.Profiles) == 0 {
		return true
	}
	for _, p := range s.Profiles {
		if active[p] {
			return true
		}
	}
	return false
}

// -- YAML shapes ----------------------------------------------------------

type file struct {
	Services map[string]serviceSpec `yaml:"services"`
	Networks map[string]networkSpec `yaml:"networks"`
}

type serviceSpec struct {
	ContainerName string           `yaml:"container_name"`
	Image         string           `yaml:"image"`
	Restart       string           `yaml:"restart"`
	Profiles      []string         `yaml:"profiles"`
	Command       yaml.Node        `yaml:"command"`
	Environment   yaml.Node        `yaml:"environment"`
	Ports         []string         `yaml:"ports"`
	Volumes       []string         `yaml:"volumes"`
	Tmpfs         yaml.Node        `yaml:"tmpfs"`
	Networks      yaml.Node        `yaml:"networks"`
	Healthcheck   *healthcheckSpec `yaml:"healthcheck"`
}

type healthcheckSpec struct {
	Test        yaml.Node `yaml:"test"`
	Interval    string    `yaml:"interval"`
	Timeout     string    `yaml:"timeout"`
	StartPeriod string    `yaml:"start_period"`
	Retries     int       `yaml:"retries"`
}

type networkSpec struct {
	External bool   `yaml:"external"`
	Name     string `yaml:"name"`
}

// Parse reads a compose file and resolves every value against lookup.
//
// Interpolation happens here rather than at use, matching Compose: the file is
// a template over the environment, and a value that failed to expand should
// fail while there is still a filename to name.
func Parse(data []byte, projectName string, lookup Lookup) (*Project, error) {
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing the compose file: %w", err)
	}

	p := &Project{Name: projectName, Networks: map[string]Network{}}

	for key, spec := range f.Networks {
		name, err := Interpolate(spec.Name, lookup)
		if err != nil {
			return nil, fmt.Errorf("network %s: %w", key, err)
		}
		if name == "" {
			name = key
		}
		p.Networks[key] = Network{Name: name, External: spec.External}
	}

	keys := make([]string, 0, len(f.Services))
	for k := range f.Services {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		svc, err := buildService(key, f.Services[key], lookup)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", key, err)
		}
		p.Services = append(p.Services, svc)
	}
	return p, nil
}

func buildService(key string, spec serviceSpec, lookup Lookup) (Service, error) {
	s := Service{Key: key, Profiles: spec.Profiles, Environment: map[string]string{}}

	var err error
	if s.ContainerName, err = Interpolate(spec.ContainerName, lookup); err != nil {
		return s, err
	}
	if s.Image, err = Interpolate(spec.Image, lookup); err != nil {
		return s, err
	}
	if s.Restart, err = Interpolate(spec.Restart, lookup); err != nil {
		return s, err
	}
	if s.Command, err = stringList(&spec.Command, lookup); err != nil {
		return s, fmt.Errorf("command: %w", err)
	}
	if s.Environment, err = envMap(&spec.Environment, lookup); err != nil {
		return s, fmt.Errorf("environment: %w", err)
	}
	if s.Ports, err = parsePorts(spec.Ports, lookup); err != nil {
		return s, err
	}
	if s.Volumes, err = parseVolumes(spec.Volumes, lookup); err != nil {
		return s, err
	}
	if s.Tmpfs, err = parseTmpfs(&spec.Tmpfs, lookup); err != nil {
		return s, fmt.Errorf("tmpfs: %w", err)
	}
	if s.Networks, err = parseNetworks(&spec.Networks, lookup); err != nil {
		return s, fmt.Errorf("networks: %w", err)
	}
	if s.Healthcheck, err = parseHealthcheck(spec.Healthcheck, lookup); err != nil {
		return s, fmt.Errorf("healthcheck: %w", err)
	}
	return s, nil
}

// stringList decodes a value that may be a single string or a sequence.
func stringList(n *yaml.Node, lookup Lookup) ([]string, error) {
	if n == nil || n.IsZero() {
		return nil, nil
	}
	switch n.Kind {
	case yaml.ScalarNode:
		v, err := Interpolate(n.Value, lookup)
		if err != nil {
			return nil, err
		}
		return []string{v}, nil
	case yaml.SequenceNode:
		var raw []string
		if err := n.Decode(&raw); err != nil {
			return nil, err
		}
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			v, err := Interpolate(item, lookup)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected a string or a list")
	}
}

// envMap decodes the mapping form (KEY: value) and the sequence form
// (- KEY=value). A sequence entry with no `=` inherits from the host
// environment, as Compose does.
func envMap(n *yaml.Node, lookup Lookup) (map[string]string, error) {
	out := map[string]string{}
	if n == nil || n.IsZero() {
		return out, nil
	}
	switch n.Kind {
	case yaml.MappingNode:
		var raw map[string]string
		if err := n.Decode(&raw); err != nil {
			return nil, err
		}
		for k, v := range raw {
			expanded, err := Interpolate(v, lookup)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = expanded
		}
	case yaml.SequenceNode:
		var raw []string
		if err := n.Decode(&raw); err != nil {
			return nil, err
		}
		for _, item := range raw {
			k, v, found := strings.Cut(item, "=")
			if !found {
				out[k] = lookup(k)
				continue
			}
			expanded, err := Interpolate(v, lookup)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = expanded
		}
	default:
		return nil, fmt.Errorf("expected a mapping or a list")
	}
	return out, nil
}

// parseNetworks decodes the list form (- name) and the mapping form
// (name: {aliases: [...]}). The bundled file uses both, and the aliases matter:
// Floci bakes the `neuronsphere` alias into the invoke and endpoint URLs it
// hands back.
func parseNetworks(n *yaml.Node, lookup Lookup) ([]NetworkAttachment, error) {
	if n == nil || n.IsZero() {
		return nil, nil
	}
	switch n.Kind {
	case yaml.SequenceNode:
		var raw []string
		if err := n.Decode(&raw); err != nil {
			return nil, err
		}
		out := make([]NetworkAttachment, 0, len(raw))
		for _, name := range raw {
			out = append(out, NetworkAttachment{Name: name})
		}
		return out, nil
	case yaml.MappingNode:
		var raw map[string]struct {
			Aliases []string `yaml:"aliases"`
		}
		if err := n.Decode(&raw); err != nil {
			return nil, err
		}
		names := make([]string, 0, len(raw))
		for k := range raw {
			names = append(names, k)
		}
		sort.Strings(names)
		out := make([]NetworkAttachment, 0, len(names))
		for _, name := range names {
			aliases := make([]string, 0, len(raw[name].Aliases))
			for _, a := range raw[name].Aliases {
				v, err := Interpolate(a, lookup)
				if err != nil {
					return nil, err
				}
				aliases = append(aliases, v)
			}
			out = append(out, NetworkAttachment{Name: name, Aliases: aliases})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected a list or a mapping")
	}
}

func parseHealthcheck(spec *healthcheckSpec, lookup Lookup) (*Healthcheck, error) {
	if spec == nil {
		return nil, nil
	}
	test, err := stringList(&spec.Test, lookup)
	if err != nil {
		return nil, fmt.Errorf("test: %w", err)
	}
	h := &Healthcheck{Test: test, Retries: spec.Retries}
	for _, d := range []struct {
		name string
		raw  string
		dst  *time.Duration
	}{
		{"interval", spec.Interval, &h.Interval},
		{"timeout", spec.Timeout, &h.Timeout},
		{"start_period", spec.StartPeriod, &h.StartPeriod},
	} {
		if d.raw == "" {
			continue
		}
		v, err := time.ParseDuration(d.raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d.name, err)
		}
		*d.dst = v
	}
	return h, nil
}

// parseVolumes handles the short bind syntax: source:target[:ro].
// parseTmpfs reads a service's tmpfs mounts, accepting compose's scalar and
// sequence spellings.
//
// Mount options -- the "/run/x:size=1m" form -- are refused rather than
// silently dropped. Nothing bundled or declared needs one, and a size limit
// that was written down and then ignored is the kind of thing discovered when
// a write fails.
func parseTmpfs(n *yaml.Node, lookup Lookup) ([]string, error) {
	paths, err := stringList(n, lookup)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		if strings.Contains(path, ":") {
			return nil, fmt.Errorf("tmpfs mount options are not supported: %q", path)
		}
		if !strings.HasPrefix(path, "/") {
			return nil, fmt.Errorf("tmpfs mount %q is not an absolute path", path)
		}
	}
	return paths, nil
}

func parseVolumes(raw []string, lookup Lookup) ([]Mount, error) {
	out := make([]Mount, 0, len(raw))
	for _, entry := range raw {
		expanded, err := Interpolate(entry, lookup)
		if err != nil {
			return nil, fmt.Errorf("volume %q: %w", entry, err)
		}
		parts := strings.Split(expanded, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return nil, fmt.Errorf("volume %q: expected source:target[:ro]", expanded)
		}
		m := Mount{Source: parts[0], Target: parts[1]}
		if !strings.HasPrefix(m.Source, "/") && !strings.HasPrefix(m.Source, ".") {
			// A named volume. Nothing bundled uses one, and treating it as a
			// host path would silently create a directory.
			return nil, fmt.Errorf("volume %q: named volumes are not supported, only bind mounts", expanded)
		}
		if len(parts) == 3 {
			switch parts[2] {
			case "ro":
				m.ReadOnly = true
			case "rw":
			default:
				return nil, fmt.Errorf("volume %q: unsupported mode %q", expanded, parts[2])
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// parsePorts handles host[:container][/proto] with ranges on either side.
func parsePorts(raw []string, lookup Lookup) ([]Port, error) {
	out := make([]Port, 0, len(raw))
	for _, entry := range raw {
		expanded, err := Interpolate(entry, lookup)
		if err != nil {
			return nil, fmt.Errorf("port %q: %w", entry, err)
		}
		p, err := parsePort(expanded)
		if err != nil {
			return nil, fmt.Errorf("port %q: %w", expanded, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func parsePort(spec string) (Port, error) {
	p := Port{Protocol: "tcp"}
	if body, proto, found := strings.Cut(spec, "/"); found {
		spec = body
		p.Protocol = proto
	}

	// An optional bind address comes first: [ip:]host:container. IPv6 is
	// bracketed, as compose writes it, so the colons inside it are not
	// mistaken for separators.
	if strings.HasPrefix(spec, "[") {
		end := strings.Index(spec, "]:")
		if end < 0 {
			return p, fmt.Errorf("unterminated IPv6 bind address")
		}
		p.HostIP = spec[1:end]
		spec = spec[end+2:]
	} else if parts := strings.Split(spec, ":"); len(parts) == 3 {
		p.HostIP = parts[0]
		spec = parts[1] + ":" + parts[2]
	}

	hostSpec, containerSpec, found := strings.Cut(spec, ":")
	if !found {
		// Container-only: Docker picks the host port. Nothing bundled does
		// this, but rejecting it would be a surprise.
		containerSpec = hostSpec
		hostSpec = ""
	}

	var err error
	if p.ContainerStart, p.ContainerEnd, err = parseRange(containerSpec); err != nil {
		return p, err
	}
	if hostSpec == "" {
		return p, nil
	}
	if p.HostStart, p.HostEnd, err = parseRange(hostSpec); err != nil {
		return p, err
	}
	if p.HostEnd-p.HostStart != p.ContainerEnd-p.ContainerStart {
		return p, fmt.Errorf("host and container ranges differ in length")
	}
	return p, nil
}

func parseRange(spec string) (int, int, error) {
	lo, hi, found := strings.Cut(spec, "-")
	start, err := strconv.Atoi(lo)
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a port number", lo)
	}
	if !found {
		return start, start, nil
	}
	end, err := strconv.Atoi(hi)
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a port number", hi)
	}
	if end < start {
		return 0, 0, fmt.Errorf("range %s ends before it starts", spec)
	}
	return start, end, nil
}

// Count is the number of host ports this Port publishes.
func (p Port) Count() int { return p.HostEnd - p.HostStart + 1 }
