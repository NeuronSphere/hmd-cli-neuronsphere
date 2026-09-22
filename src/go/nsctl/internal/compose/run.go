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
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"

	"github.com/docker/go-connections/nat"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dockerhost"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	nsdocker "github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
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
	LabelWorkingDir      = "com.docker.compose.project.working_dir"
	LabelConfigFiles     = "com.docker.compose.project.config_files"
)

// LabelComposeConfigHash is what compose uses to decide a container is one of
// its own. Its *presence* is what makes a container discoverable: without it
// `docker compose ps|stop|down` skips the container entirely even when every
// other label matches, which was verified against compose v2.40 -- so omitting
// it would have left `hmd neuronsphere down` silently stopping nothing.
//
// nsctl writes its own hash here rather than reproducing compose's algorithm,
// which is an internal detail that changes across versions. The value is only
// ever compared by compose on `up`, so the consequence is bounded and stated
// rather than hidden: switching front ends recreates each container once,
// because neither tool recognises the other's hash. That is benign here -- all
// three keep their state in binds or Floci volumes, not the writable layer.
const LabelComposeConfigHash = "com.docker.compose.config-hash"

// LabelConfigHash is nsctl's own copy, which is the one nsctl compares. Keeping
// it separate means a container compose rebuilt (carrying compose's hash and no
// nsctl label) reads as changed rather than accidentally matching.
const LabelConfigHash = "io.neuronsphere.nsctl.config-hash"

// dockerAPI is the Engine API surface the runner uses. Narrowing it to these
// calls is what makes the create/start/recreate decision testable without a
// daemon -- the same reason hmd-cli-bartleby narrows its client to five methods.
type dockerAPI interface {
	ContainerInspect(ctx context.Context, name string) (types.ContainerJSON, error)
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error)
	ContainerList(ctx context.Context, options container.ListOptions) ([]types.Container, error)
	ContainerStart(ctx context.Context, name string, options container.StartOptions) error
	ContainerStop(ctx context.Context, name string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, name string, options container.RemoveOptions) error
	ContainerExecCreate(ctx context.Context, name string, options container.ExecOptions) (types.IDResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, options container.ExecStartOptions) (types.HijackedResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
	ImageInspectWithRaw(ctx context.Context, ref string) (types.ImageInspect, []byte, error)
	NetworkInspect(ctx context.Context, name string, options network.InspectOptions) (network.Inspect, error)
	NetworkCreate(ctx context.Context, name string, options network.CreateOptions) (network.CreateResponse, error)
}

// Puller fetches an image reference.
//
// Injected because the Engine API cannot do it. ImagePull sends whatever
// credentials the caller puts in PullOptions and nothing else -- the daemon
// never reads ~/.docker/config.json, which is the CLI's job -- so a pull with
// empty options is credential-less, and ghcr.io answers a credential-less
// request with `unauthorized` even for a public image. That failed the first
// genuinely cold start here, on an image that pulls anonymously from the CLI
// without complaint, and it had stayed invisible because the image is in the
// cache of every machine that has ever run the platform.
//
// The remedy is the path the rest of nsctl already uses: container.PullImage
// shells out to `docker pull`, which resolves credential helpers, keychains and
// anonymous tokens exactly as the user's own `docker pull` would.
type Puller func(ctx context.Context, ref string) error

// Runner creates and reconciles a project's containers.
type Runner struct {
	API dockerAPI
	// PullImage fetches an image. NewRunner supplies the CLI-backed one; a
	// test may substitute its own.
	PullImage Puller
	// Endpoint is where this runner is connected, so a failure can say which
	// engine refused rather than making the reader guess.
	Endpoint dockerhost.Endpoint
	// Out carries progress; Err carries note: and warning: lines.
	Out io.Writer
	Err io.Writer
}

// NewRunner connects to the engine the user's own docker CLI reaches.
//
// Not client.FromEnv: it honours DOCKER_HOST and nothing else, so on any
// machine whose current context names a socket elsewhere it fell back to
// unix:///var/run/docker.sock. That is every Colima and OrbStack install --
// and Docker Desktop too, whose context is unix://$HOME/.docker/run/docker.sock
// and which only worked here because it symlinks the legacy path. The CLI-shaped
// preflight passed and this failed four steps later (NERD021 SPEC001).
func NewRunner(ctx context.Context, out, errOut io.Writer) (*Runner, error) {
	ep, _ := (&dockerhost.Resolver{Inspect: dockerhost.CLIInspector(nil)}).Resolve(ctx)
	return NewRunnerAt(ep, nil, out, errOut)
}

// NewRunnerAt builds a Runner against an already-resolved endpoint, so a
// command that resolved one in its preflight proves and uses the same engine
// rather than resolving a second time.
func NewRunnerAt(ep dockerhost.Endpoint, lookup hmdenv.Lookup, out, errOut io.Writer) (*Runner, error) {
	opts, err := ep.ClientOpts(lookup)
	if err != nil {
		return nil, err
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("connecting to the container engine at %s: %w", ep.Describe(), err)
	}
	return &Runner{API: cli, Endpoint: ep, PullImage: nsdocker.New().PullImage, Out: out, Err: errOut}, nil
}

// pull fetches an image through the injected puller.
//
// A Runner built by hand with no Puller cannot pull at all, and says so rather
// than falling back to the Engine API: that fallback is precisely the
// credential-less path this field exists to avoid, and a silent fallback would
// reintroduce the failure on exactly the machines that cannot afford it.
func (r *Runner) pull(ctx context.Context, ref string) error {
	if r.PullImage == nil {
		return fmt.Errorf("pulling %s: this runner has no puller", ref)
	}
	if err := r.PullImage(ctx, ref); err != nil {
		return fmt.Errorf("pulling %s: %w", ref, err)
	}
	return nil
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
	// Err is why this service did not start, and is only ever set by UpEach.
	// Up returns on the first failure and reports it as its error, because for
	// the control plane's own services a partial start is not a success.
	Err error
}

// Failed reports that this service did not start.
func (r Result) Failed() bool { return r.Err != nil }

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
		return fmt.Errorf("creating the %s network on %s: %w", name, r.Endpoint.Describe(), err)
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

// UpEach is Up with per-service failure isolation: every enabled service is
// attempted, and one that fails is recorded in its own Result rather than
// ending the run.
//
// This exists for NERD004 SPEC006. Up is right for the five bundled services --
// a proxy that will not start is not a partial success -- and wrong the moment
// a service arrives that nobody vouched for. Services are iterated in sorted
// key order, so under Up a single unpullable extension image would abort the
// control plane's own containers, and which ones survived would depend on
// where the extension's name happened to sort.
//
// The returned error is reserved for something that failed the whole project
// rather than one service; there is nothing of that kind today, and the
// signature keeps the door open without callers having to guess.
func (r *Runner) UpEach(ctx context.Context, p *Project, active map[string]bool) ([]Result, error) {
	var results []Result
	for _, s := range p.Services {
		if !s.EnabledBy(active) {
			results = append(results, Result{Service: s.Key, Name: s.Name(p.Name), Action: ActionSkipped})
			continue
		}
		res, err := r.upService(ctx, p, s)
		if err != nil {
			// Name it here rather than at the call site: the caller has a list
			// of results and no idea which service each error came from.
			res.Service, res.Name, res.Err = s.Key, s.Name(p.Name), err
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
		//
		// Recreating Floci is not free: it spawns and supervises the RDS and
		// Neptune containers backing every account, and its recovery of them on
		// restart is not reliable -- an instance whose container it cannot
		// bring back is left reporting `failed`, with a redeploy the only way
		// out. Observed on the first nsctl start against a platform the Python
		// CLI created, where no nsctl hash label existed and every container
		// was therefore recreated. Say so before doing it rather than leaving
		// the consequence to be discovered as a service answering 500.
		if s.Key == FlociService {
			r.warn("recreating %s. Floci's recovery of the databases it spawned is not reliable; if one comes back in state \"failed\", redeploy it with `hmd neuronsphere up --env <name>`.", name)
		}
		if err := r.API.ContainerRemove(ctx, existing.ID, container.RemoveOptions{Force: true}); err != nil {
			return res, fmt.Errorf("removing the outdated container: %w", err)
		}
		res.Action = ActionRecreated
	case client.IsErrNotFound(err):
		res.Action = ActionCreated
	default:
		return res, fmt.Errorf("inspecting %s: %w", name, err)
	}

	if err := r.EnsureImage(ctx, s.Image); err != nil {
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
//
// It takes no profile set, deliberately. What has to be stopped is whatever is
// *running*, which is not the same as whatever is currently configured: a
// container started while a profile was active survives a stop run without it,
// which is how `hmd_nsrunner` outlived `hmd neuronsphere down --purge` on the
// Python side (its COMPOSE_PROFILES named only the GUI). Every service in the
// project is stopped, profile-gated or not, and a service with no container is
// not an error.
func (r *Runner) Stop(ctx context.Context, p *Project) error {
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

// It does not pull when the image is present, even for a floating tag. That is
// `docker compose up`'s behaviour too, and it is what makes an offline start
// work; `--upgrade` is the explicit way to ask for newer images.
// Adopted is every container labelled with this project that the given project
// does not describe, as service key -> container name.
//
// It is how the control plane stops and removes its extensions. Naming them
// from a re-resolved manifest would not do: a container whose repo class has
// since been deleted, or whose declaration has been removed, still has to be
// stoppable -- and it is exactly the container a name-driven sweep would miss
// and leave running against a network that is about to go away.
//
// A daemon that cannot be asked yields nothing rather than an error. This
// drives a stop, and an empty inventory is safer than refusing to stop the
// control plane because a list could not be read.
func (r *Runner) Adopted(ctx context.Context, p *Project) map[string]string {
	if p == nil || p.Name == "" {
		return nil
	}
	known := map[string]bool{}
	for _, s := range p.Services {
		known[s.Key] = true
	}
	f := filters.NewArgs()
	f.Add("label", LabelProject+"="+p.Name)
	list, err := r.API.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, c := range list {
		key := c.Labels[LabelService]
		if key == "" || known[key] {
			continue
		}
		for _, name := range c.Names {
			out[key] = strings.TrimPrefix(name, "/")
			break
		}
	}
	return out
}

// StopNamed stops the given containers, accumulating failures rather than
// returning at the first -- Stop's reason: a sweep that stops at the first
// container it cannot reach is how the others accumulate.
func (r *Runner) StopNamed(ctx context.Context, names []string) error {
	return r.eachNamed(ctx, names, "stopping", "stopped", func(name string) error {
		return r.API.ContainerStop(ctx, name, container.StopOptions{})
	})
}

// RemoveNamed force-removes the given containers.
func (r *Runner) RemoveNamed(ctx context.Context, names []string) error {
	return r.eachNamed(ctx, names, "removing", "removed", func(name string) error {
		return r.API.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
	})
}

func (r *Runner) eachNamed(ctx context.Context, names []string, verb, past string, do func(string) error) error {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	var failed []string
	for _, name := range sorted {
		if err := do(name); err != nil {
			if client.IsErrNotFound(err) {
				continue
			}
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		r.progress("  %s %s", name, past)
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s containers: %s", verb, strings.Join(failed, "; "))
	}
	return nil
}

// WriteInto writes bytes to a path inside a running container, through an exec
// reading standard input.
//
// **Not** CopyToContainer, and the reason is the whole point of NERD004
// SPEC009. `docker cp` resolves its destination in the container rootfs as the
// daemon sees it, where a tmpfs mounted *inside* the container does not exist
// -- so a copy to /run/ns-secrets lands in the writable layer underneath the
// mount. That is invisible to the container, which is how it goes unnoticed,
// and it is on disk, which is the one outcome this SPEC exists to prevent.
// `docker diff` reported `A /run/ns-secrets/upstream` on the first real run of
// it.
//
// An exec runs in the container's own mount namespace, so it sees the tmpfs.
// The value travels on standard input rather than in argv, so it appears in no
// process listing; only the destination path does. Nothing is written to the
// host filesystem at any point.
//
// A container with no shell cannot be written to this way, and says so.
func (r *Runner) WriteInto(ctx context.Context, name, path string, data []byte) error {
	created, err := r.API.ContainerExecCreate(ctx, name, container.ExecOptions{
		// "$1" rather than interpolation: a path is not a place to start
		// quoting by hand. umask keeps the file 0600 on a tmpfs shared with
		// whatever else the container runs.
		Cmd:          []string{"/bin/sh", "-c", `umask 077; cat > "$1"`, "sh", path},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("preparing to write %s in %s: %w", path, name, err)
	}

	attached, err := r.API.ContainerExecAttach(ctx, created.ID, container.ExecStartOptions{})
	if err != nil {
		return fmt.Errorf("writing %s in %s: %w", path, name, err)
	}
	defer attached.Close()

	if _, err := attached.Conn.Write(data); err != nil {
		return fmt.Errorf("writing %s in %s: %w", path, name, err)
	}
	// Without the half-close `cat` never sees EOF and the exec never exits.
	if err := attached.CloseWrite(); err != nil {
		return fmt.Errorf("writing %s in %s: %w", path, name, err)
	}
	// Drained so the exec completes. The output is diagnostics and never the
	// value: the command echoes nothing.
	output, _ := io.ReadAll(attached.Reader)

	inspect, err := r.API.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return fmt.Errorf("confirming the write of %s in %s: %w", path, name, err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("writing %s in %s failed (exit %d): %s",
			path, name, inspect.ExitCode, firstLine(output))
	}
	return nil
}

// firstLine trims exec output to something that fits in an error. Exec output
// is stream-multiplexed when no TTY is attached, so it starts with a frame
// header rather than text.
func firstLine(out []byte) string {
	text := strings.TrimSpace(string(out))
	if i := strings.IndexByte(text, 10); i >= 0 {
		text = text[:i]
	}
	text = strings.TrimFunc(text, func(r rune) bool { return r < 32 })
	if text == "" {
		return "no output"
	}
	return text
}

// EnsureImage pulls the image when it is not already local.
func (r *Runner) EnsureImage(ctx context.Context, ref string) error {
	if ref == "" {
		return errors.New("no image")
	}
	if _, _, err := r.API.ImageInspectWithRaw(ctx, ref); err == nil {
		return nil
	}
	r.progress("Pulling %s...", ref)
	return r.pull(ctx, ref)
}

// Pull fetches a service's image whether or not it is already local. This is
// what `--upgrade` uses.
func (r *Runner) Pull(ctx context.Context, ref string) error {
	r.progress("Pulling %s...", ref)
	return r.pull(ctx, ref)
}

func (r *Runner) warn(format string, a ...any) {
	if r.Err == nil {
		return
	}
	fmt.Fprintf(r.Err, "warning: "+format+"\n", a...)
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
		Tmpfs:         tmpfs(s.Tmpfs),
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
	out := map[string]string{
		LabelProject:           p.Name,
		LabelService:           s.Key,
		LabelContainerNumber:   "1",
		LabelOneOff:            "False",
		LabelComposeConfigHash: hash,
		LabelConfigHash:        hash,
	}
	if p.WorkingDir != "" {
		out[LabelWorkingDir] = p.WorkingDir
	}
	if p.ConfigFile != "" {
		out[LabelConfigFiles] = p.ConfigFile
	}
	return out
}

// tmpfs renders the in-memory mounts, with Docker's default options.
func tmpfs(paths []string) map[string]string {
	if len(paths) == 0 {
		return nil
	}
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		out[path] = ""
	}
	return out
}

// binds renders the mounts, dropping exact duplicates.
//
// Two entries can interpolate to the same pair -- the runner mounts HMD_HOME
// and HMD_REPO_HOME at their own paths, and those are the same directory
// whenever repos live under the home. Docker rejects a duplicate mount point
// outright, so a container that is merely over-specified would fail to be
// created at all.
func binds(mounts []Mount) []string {
	out := make([]string, 0, len(mounts))
	seen := make(map[string]bool, len(mounts))
	for _, m := range mounts {
		bind := m.Source + ":" + m.Target
		if m.ReadOnly {
			bind += ":ro"
		}
		if seen[bind] {
			continue
		}
		seen[bind] = true
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

// labelSchema versions the label set nsctl stamps. It is part of the hash, so
// changing which labels are written forces one recreate rather than leaving
// existing containers carrying the old set forever -- which is exactly what
// happened when com.docker.compose.config-hash was added: the containers kept
// their old labels and stayed invisible to `docker compose`.
// 3 adds tmpfs to the hashed payload (NERD004 SPEC009). Every existing
// container's hash changes with it, so each recreates once on the first start
// after this lands -- deliberate rather than incidental: a tmpfs added to a
// running container's declaration must actually take effect, and it cannot
// without a recreate.
const labelSchema = 3

// configHash fingerprints everything that would require a recreate. A container
// whose hash still matches is started in place rather than rebuilt, which is
// what makes a restart cheap.
// ConfigHash is the digest upService compares to decide a container is still
// the one the project describes. Exported so a caller that mutates a parsed
// project -- adding an extension's hostname to the proxy's aliases -- can
// assert the change really does force a recreate, rather than assume it.
func ConfigHash(p *Project, s Service) string { return configHash(p, s) }

func configHash(p *Project, s Service) string {
	// A struct rendered through encoding/json, whose object keys are sorted, so
	// the hash does not depend on map iteration order.
	payload := struct {
		LabelSchema int                 `json:"label_schema"`
		Project     string              `json:"project"`
		Service     string              `json:"service"`
		WorkingDir  string              `json:"working_dir"`
		ConfigFile  string              `json:"config_file"`
		Image       string              `json:"image"`
		Command     []string            `json:"command"`
		Environment map[string]string   `json:"environment"`
		Ports       []Port              `json:"ports"`
		Volumes     []Mount             `json:"volumes"`
		Tmpfs       []string            `json:"tmpfs"`
		Networks    []NetworkAttachment `json:"networks"`
		Healthcheck *Healthcheck        `json:"healthcheck"`
		Restart     string              `json:"restart"`
	}{
		LabelSchema: labelSchema,
		Project:     p.Name, Service: s.Key, Image: s.Image, Command: s.Command,
		WorkingDir: p.WorkingDir, ConfigFile: p.ConfigFile,
		Environment: s.Environment, Ports: s.Ports, Volumes: s.Volumes,
		Tmpfs:       s.Tmpfs,
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
