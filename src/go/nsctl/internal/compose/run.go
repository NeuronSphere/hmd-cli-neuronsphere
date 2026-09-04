package compose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Compose's own labels. nsctl stamps them so the Python CLI's
// `docker compose ps|stop|down` still finds containers nsctl created, and so
// port_validator.py -- which filters on com.docker.compose.project -- keeps
// working. Both front ends operate on the same state and must stay
// interchangeable mid-port (SPEC012).
const (
	LabelProject         = "com.docker.compose.project"
	LabelService         = "com.docker.compose.service"
	LabelContainerNumber = "com.docker.compose.container-number"
	LabelOneOff          = "com.docker.compose.oneoff"
)

// LabelConfigHash is nsctl's own. Compose keeps a config-hash of its own that
// nsctl deliberately does not try to reproduce: matching it would mean matching
// an internal detail of another tool across versions. The cost is stated rather
// than hidden -- alternating front ends may recreate a container once, which is
// benign here because all three keep their state in binds or Floci volumes
// rather than the writable layer.
const LabelConfigHash = "io.neuronsphere.nsctl.config-hash"

// dockerAPI is the Engine API surface the runner uses. Narrowing it to these
// calls is what makes the create/start/recreate decision testable without a
// daemon -- the same reason hmd-cli-bartleby narrows its client to five methods.
type dockerAPI interface {
	ContainerInspect(ctx context.Context, name string) (types.ContainerJSON, error)
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, name string, options container.StartOptions) error
	ContainerStop(ctx context.Context, name string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, name string, options container.RemoveOptions) error
	ImageInspectWithRaw(ctx context.Context, ref string) (types.ImageInspect, []byte, error)
	ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error)
	NetworkInspect(ctx context.Context, name string, options network.InspectOptions) (network.Inspect, error)
	NetworkCreate(ctx context.Context, name string, options network.CreateOptions) (network.CreateResponse, error)
}

// Runner creates and reconciles a project's containers.
type Runner struct {
	API dockerAPI
	// Out carries progress; Err carries note: and warning: lines.
	Out io.Writer
	Err io.Writer
}

// NewRunner connects to the local Docker daemon.
func NewRunner(out, errOut io.Writer) (*Runner, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("connecting to Docker: %w", err)
	}
	return &Runner{API: cli, Out: out, Err: errOut}, nil
}

// Action is what Up did to one service.
type Action string

const (
	// ActionStarted means an existing, unchanged container was started in
	// place. This is the warm-restart path and the common one: after
	// `env stop`, `control-plane start` should not rebuild anything.
	ActionStarted Action = "started"
	// ActionRunning means it was already up.
	ActionRunning Action = "running"
	// ActionCreated means there was no container.
	ActionCreated Action = "created"
	// ActionRecreated means the configuration changed.
	ActionRecreated Action = "recreated"
	// ActionSkipped means a profile gate excluded it.
	ActionSkipped Action = "skipped"
)

// Result reports what happened to one service.
type Result struct {
	Service string
	Name    string
	Action  Action
}

// EnsureNetwork creates the project's external network if it is missing.
//
// The compose file declares it external, which means compose expects it to
// already exist; the Python CLI runs `docker network create` before `up` for
// exactly that reason. `down` deliberately leaves it in place so stopped
// containers can restart onto it.
func (r *Runner) EnsureNetwork(ctx context.Context, name string) error {
	if _, err := r.API.NetworkInspect(ctx, name, network.InspectOptions{}); err == nil {
		return nil
	}
	if _, err := r.API.NetworkCreate(ctx, name, network.CreateOptions{Driver: "bridge"}); err != nil {
		return fmt.Errorf("creating the %s network: %w", name, err)
	}
	r.progress("Created the %s network.", name)
	return nil
}

// Up brings every service enabled by the active profiles to running.
func (r *Runner) Up(ctx context.Context, p *Project, active map[string]bool) ([]Result, error) {
	var results []Result
	for _, s := range p.Services {
		if !s.EnabledBy(active) {
			results = append(results, Result{Service: s.Key, Name: s.Name(p.Name), Action: ActionSkipped})
			continue
		}
		res, err := r.upService(ctx, p, s)
		if err != nil {
			return results, fmt.Errorf("starting %s: %w", s.Key, err)
		}
		results = append(results, res)
	}
	return results, nil
}

func (r *Runner) upService(ctx context.Context, p *Project, s Service) (Result, error) {
	name := s.Name(p.Name)
	res := Result{Service: s.Key, Name: name}
	want := configHash(p, s)

	existing, err := r.API.ContainerInspect(ctx, name)
	switch {
	case err == nil:
		if existing.Config != nil && existing.Config.Labels[LabelConfigHash] == want {
			if existing.State != nil && existing.State.Running {
				res.Action = ActionRunning
				return res, nil
			}
			// The warm restart: same configuration, just stopped.
			if err := r.API.ContainerStart(ctx, existing.ID, container.StartOptions{}); err != nil {
				return res, fmt.Errorf("starting the existing container: %w", err)
			}
			res.Action = ActionStarted
			return res, nil
		}
		// Configuration changed, so the container has to go.
		if err := r.API.ContainerRemove(ctx, existing.ID, container.RemoveOptions{Force: true}); err != nil {
			return res, fmt.Errorf("removing the outdated container: %w", err)
		}
		res.Action = ActionRecreated
	case client.IsErrNotFound(err):
		res.Action = ActionCreated
	default:
		return res, fmt.Errorf("inspecting %s: %w", name, err)
	}

	if err := r.ensureImage(ctx, s.Image); err != nil {
		return res, err
	}
	cfg, hostCfg, netCfg, err := containerSpec(p, s, want)
	if err != nil {
		return res, err
	}
	created, err := r.API.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, name)
	if err != nil {
		return res, fmt.Errorf("creating %s: %w", name, err)
	}
	if err := r.API.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return res, fmt.Errorf("starting %s: %w", name, err)
	}
	return res, nil
}

// Stop stops the project's containers without removing them, so the next start
// restarts them in place. Matches `docker compose stop`, which is what
// `hmd neuronsphere down` runs: a stop, not a teardown.
func (r *Runner) Stop(ctx context.Context, p *Project, active map[string]bool) error {
	var failed []string
	for _, s := range p.Services {
		name := s.Name(p.Name)
		if err := r.API.ContainerStop(ctx, name, container.StopOptions{}); err != nil {
			if client.IsErrNotFound(err) {
				continue
			}
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("stopping containers: %s", strings.Join(failed, "; "))
	}
	return nil
}

// Remove stops and removes the project's containers. Only a purge does this.
func (r *Runner) Remove(ctx context.Context, p *Project) error {
	var failed []string
	for _, s := range p.Services {
		name := s.Name(p.Name)
		if err := r.API.ContainerRemove(ctx, name, container.RemoveOptions{Force: true}); err != nil {
			if client.IsErrNotFound(err) {
				continue
			}
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("removing containers: %s", strings.Join(failed, "; "))
	}
	return nil
}

// ensureImage pulls the image when it is not already local.
//
// It does not pull when the image is present, even for a floating tag. That is
// `docker compose up`'s behaviour too, and it is what makes an offline start
// work; `--upgrade` is the explicit way to ask for newer images.
func (r *Runner) ensureImage(ctx context.Context, ref string) error {
	if ref == "" {
		return errors.New("no image")
	}
	if _, _, err := r.API.ImageInspectWithRaw(ctx, ref); err == nil {
		return nil
	}
	r.progress("Pulling %s...", ref)
	body, err := r.API.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pulling %s: %w", ref, err)
	}
	defer body.Close()
	// The pull is only complete once the stream is drained.
	if _, err := io.Copy(io.Discard, body); err != nil {
		return fmt.Errorf("pulling %s: %w", ref, err)
	}
	return nil
}

// Pull fetches a service's image whether or not it is already local. This is
// what `--upgrade` uses.
func (r *Runner) Pull(ctx context.Context, ref string) error {
	r.progress("Pulling %s...", ref)
	body, err := r.API.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pulling %s: %w", ref, err)
	}
	defer body.Close()
	_, err = io.Copy(io.Discard, body)
	return err
}

func (r *Runner) progress(format string, a ...any) {
	if r.Out == nil {
		return
	}
	fmt.Fprintf(r.Out, format+"\n", a...)
}

// containerSpec translates one Service into the three Engine API structs.
func containerSpec(p *Project, s Service, hash string) (*container.Config, *container.HostConfig, *network.NetworkingConfig, error) {
	exposed, bindings, err := portSpec(s.Ports)
	if err != nil {
		return nil, nil, nil, err
	}

	env := make([]string, 0, len(s.Environment))
	for _, k := range s.EnvKeys() {
		env = append(env, k+"="+s.Environment[k])
	}

	cfg := &container.Config{
		Image:        s.Image,
		Env:          env,
		Labels:       labels(p, s, hash),
		ExposedPorts: exposed,
	}
	if len(s.Command) > 0 {
		cfg.Cmd = strslice.StrSlice(s.Command)
	}
	if s.Healthcheck != nil {
		cfg.Healthcheck = &container.HealthConfig{
			Test:        s.Healthcheck.Test,
			Interval:    s.Healthcheck.Interval,
			Timeout:     s.Healthcheck.Timeout,
			StartPeriod: s.Healthcheck.StartPeriod,
			Retries:     s.Healthcheck.Retries,
		}
	}

	hostCfg := &container.HostConfig{
		PortBindings:  bindings,
		Binds:         binds(s.Volumes),
		RestartPolicy: restartPolicy(s.Restart),
	}

	netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	for _, attach := range s.Networks {
		real := attach.Name
		if n, ok := p.Networks[attach.Name]; ok && n.Name != "" {
			real = n.Name
		}
		netCfg.EndpointsConfig[real] = &network.EndpointSettings{Aliases: attach.Aliases}
	}

	return cfg, hostCfg, netCfg, nil
}

func labels(p *Project, s Service, hash string) map[string]string {
	return map[string]string{
		LabelProject:         p.Name,
		LabelService:         s.Key,
		LabelContainerNumber: "1",
		LabelOneOff:          "False",
		LabelConfigHash:      hash,
	}
}

func binds(mounts []Mount) []string {
	out := make([]string, 0, len(mounts))
	for _, m := range mounts {
		bind := m.Source + ":" + m.Target
		if m.ReadOnly {
			bind += ":ro"
		}
		out = append(out, bind)
	}
	return out
}

func restartPolicy(spec string) container.RestartPolicy {
	switch spec {
	case "", "no":
		return container.RestartPolicy{Name: container.RestartPolicyDisabled}
	case "always":
		return container.RestartPolicy{Name: container.RestartPolicyAlways}
	case "unless-stopped":
		return container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}
	case "on-failure":
		return container.RestartPolicy{Name: container.RestartPolicyOnFailure}
	default:
		return container.RestartPolicy{Name: container.RestartPolicyDisabled}
	}
}

// portSpec expands ranges into individual bindings, which is what the Engine
// API takes -- the 19000-19079 band becomes eighty entries.
func portSpec(ports []Port) (nat.PortSet, nat.PortMap, error) {
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, p := range ports {
		for i := 0; i <= p.ContainerEnd-p.ContainerStart; i++ {
			cPort, err := nat.NewPort(p.Protocol, strconv.Itoa(p.ContainerStart+i))
			if err != nil {
				return nil, nil, fmt.Errorf("port %d/%s: %w", p.ContainerStart+i, p.Protocol, err)
			}
			exposed[cPort] = struct{}{}
			if p.HostEnd == 0 && p.HostStart == 0 {
				// Container-only: let Docker choose.
				continue
			}
			bindings[cPort] = append(bindings[cPort], nat.PortBinding{
				HostPort: strconv.Itoa(p.HostStart + i),
			})
		}
	}
	return exposed, bindings, nil
}

// configHash fingerprints everything that would require a recreate. A container
// whose hash still matches is started in place rather than rebuilt, which is
// what makes a restart cheap.
func configHash(p *Project, s Service) string {
	// A struct rendered through encoding/json, whose object keys are sorted, so
	// the hash does not depend on map iteration order.
	payload := struct {
		Project     string              `json:"project"`
		Service     string              `json:"service"`
		Image       string              `json:"image"`
		Command     []string            `json:"command"`
		Environment map[string]string   `json:"environment"`
		Ports       []Port              `json:"ports"`
		Volumes     []Mount             `json:"volumes"`
		Networks    []NetworkAttachment `json:"networks"`
		Healthcheck *Healthcheck        `json:"healthcheck"`
		Restart     string              `json:"restart"`
	}{
		Project: p.Name, Service: s.Key, Image: s.Image, Command: s.Command,
		Environment: s.Environment, Ports: s.Ports, Volumes: s.Volumes,
		Healthcheck: s.Healthcheck, Restart: s.Restart,
	}
	// Networks resolved to their real names, sorted: a renamed network must
	// force a recreate.
	for _, attach := range s.Networks {
		real := attach.Name
		if n, ok := p.Networks[attach.Name]; ok && n.Name != "" {
			real = n.Name
		}
		aliases := append([]string(nil), attach.Aliases...)
		sort.Strings(aliases)
		payload.Networks = append(payload.Networks, NetworkAttachment{Name: real, Aliases: aliases})
	}
	sort.Slice(payload.Networks, func(i, j int) bool { return payload.Networks[i].Name < payload.Networks[j].Name })

	data, err := json.Marshal(payload)
	if err != nil {
		// Marshalling these types cannot fail; if it somehow did, a hash that
		// never matches is the safe answer -- it recreates rather than
		// silently reusing a container built from something else.
		return "unhashable"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
