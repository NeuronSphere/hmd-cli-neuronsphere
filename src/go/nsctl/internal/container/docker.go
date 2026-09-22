package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Floci labels every container it manages. The backing container's *name* is
// opaque (floci-rds-db-<HEX>-<suffix>) and not derived from the resource
// identifier, so these labels -- not the name -- are the way to find it.
const (
	LabelFlociService  = "io.floci.service"
	LabelFlociAccount  = "io.floci.account"
	LabelFlociResource = "io.floci.resource-id"
	// LabelFloci is on every container Floci spawns, whatever the service, and
	// on nothing else -- the compose-created `floci` container itself does not
	// carry it. It is the only handle on the services a purge does not name
	// individually: ECR, Lambda, and whatever Floci grows next.
	LabelFloci = "floci"
)

// ErrDockerMissing is returned when the docker CLI is not on PATH.
var ErrDockerMissing = errors.New("docker not found on PATH")

// Docker runs the docker CLI. Shelling out rather than using the Engine API for
// these reads keeps the dependency surface small and the commands quotable in a
// bug report; internal/compose reaches for the API where parsing output would
// be fragile.
type Docker struct {
	Bin     string
	Timeout time.Duration
}

// New returns a Docker with the usual binary and timeout.
func New() *Docker {
	return &Docker{Bin: "docker", Timeout: 15 * time.Second}
}

func (d *Docker) bin() string {
	if d.Bin == "" {
		return "docker"
	}
	return d.Bin
}

// Available reports whether the docker CLI can be found and answers.
func (d *Docker) Available(ctx context.Context) error {
	if _, err := exec.LookPath(d.bin()); err != nil {
		return ErrDockerMissing
	}
	if _, err := d.capture(ctx, "version", "--format", "{{.Server.Version}}"); err != nil {
		return fmt.Errorf("docker is installed but not responding (is the daemon running?): %w", err)
	}
	return nil
}

// ContextEndpoint is the daemon endpoint the docker CLI itself resolves, as
// "<context>\t<host>\t<skipTLSVerify>".
//
// The CLI, rather than a reimplementation of ~/.docker/contexts: it already
// resolves DOCKER_HOST, DOCKER_CONTEXT, currentContext and DOCKER_CONFIG, and
// it is already a hard prerequisite here -- Available, PullImage, the deploy
// runner and every k3s exec all shell out to it. This is the reasoning
// compose's Puller records for `docker pull`: where the CLI resolves something
// the Engine API client does not, ask the CLI (NERD021 SPEC002).
func (d *Docker) ContextEndpoint(ctx context.Context) (string, error) {
	return d.capture(ctx, "context", "inspect", "--format",
		"{{.Name}}\t{{.Endpoints.docker.Host}}\t{{.Endpoints.docker.SkipTLSVerify}}")
}

// capture runs a docker subcommand and returns its trimmed stdout.
func (d *Docker) capture(ctx context.Context, args ...string) (string, error) {
	if d.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.Timeout)
		defer cancel()
	}
	var out, errBuf bytes.Buffer
	c := exec.CommandContext(ctx, d.bin(), args...)
	c.Stdout = &out
	c.Stderr = &errBuf
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("docker %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// Running reports whether a container of that exact name exists and is running.
//
// A missing container is (false, nil), not an error: "not running" and "not
// there" are the same answer to the question status asks, and an install with
// no graph provisioned is a normal state rather than a fault.
func (d *Docker) Running(ctx context.Context, name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	out, err := d.capture(ctx, "inspect", "-f", "{{.State.Running}}", name)
	if err != nil {
		return false, nil
	}
	return out == "true", nil
}

// Exists reports whether a container of that name exists at all, running or not.
func (d *Docker) Exists(ctx context.Context, name string) bool {
	if name == "" {
		return false
	}
	_, err := d.capture(ctx, "inspect", "-f", "{{.Id}}", name)
	return err == nil
}

// ContainerNames lists every container on the host, running or not. Ports
// k3s_container_name's _existing_container_names.
func (d *Docker) ContainerNames(ctx context.Context) map[string]bool {
	out, err := d.capture(ctx, "ps", "-a", "--format", "{{.Names}}")
	names := map[string]bool{}
	if err != nil {
		return names
	}
	for _, n := range strings.Fields(out) {
		names[n] = true
	}
	return names
}

// FlociContainer finds the container Floci spawned for one of its managed
// resources, reporting its name and whether it is running. An empty name means
// there is none.
//
// The label triple is the only reliable lookup. floci_deployer's own comment
// says why: Floci names these containers opaquely
// (floci-rds-db-<HEX>-<suffix>), and `docker inspect` on the DNS alias attached
// later does not work, because an alias is not an object.
//
// This searches `docker ps -a`, where floci_deployer.floci_container_name
// searches only running containers. That function feeds callers that need a
// container to exec into, for which "stopped" and "absent" really are the same
// answer. Status is the case where they are not: after `nsctl env stop`, a
// stopped database is the expected state, and reporting it as unprovisioned
// would read as data loss.
func (d *Docker) FlociContainer(ctx context.Context, service, accountID, resourceID string) (string, bool) {
	out, err := d.capture(ctx, flociPSArgs(service, accountID, resourceID)...)
	if err != nil || out == "" {
		return "", false
	}
	// Floci can leave more than one backing container behind for the same
	// resource id across restarts, so a running one wins over a stale stopped
	// one. Taking the first line would otherwise report a resource stopped
	// while the container actually serving it is up.
	var fallback string
	for _, line := range strings.Split(out, "\n") {
		name, state, _ := strings.Cut(line, "\t")
		name, state = strings.TrimSpace(name), strings.TrimSpace(state)
		if name == "" {
			continue
		}
		if state == "running" {
			return name, true
		}
		if fallback == "" {
			fallback = name
		}
	}
	return fallback, false
}

// flociPSArgs builds the `docker ps` invocation that finds a Floci-managed
// container.
//
// Split out to be testable. Every value needs the literal "label=" prefix;
// without it docker rejects the filter, and because a rejected filter is an
// error rather than an empty result, the caller reported every database and
// graph as never provisioned -- which reads as data loss rather than a typo.
func flociPSArgs(service, accountID, resourceID string) []string {
	return []string{
		"ps", "-a", "--format", "{{.Names}}\t{{.State}}",
		"--filter", "label=" + LabelFlociService + "=" + service,
		"--filter", "label=" + LabelFlociAccount + "=" + accountID,
		"--filter", "label=" + LabelFlociResource + "=" + resourceID,
	}
}

// NetworkExists reports whether a Docker network of that name exists.
func (d *Docker) NetworkExists(ctx context.Context, name string) bool {
	if name == "" {
		return false
	}
	_, err := d.capture(ctx, "network", "inspect", "-f", "{{.Id}}", name)
	return err == nil
}

// Exec runs a command inside a running container and returns its combined
// output. It is the seam internal/router reloads nginx through.
func (d *Docker) Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if d.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.Timeout)
		defer cancel()
	}
	full := append([]string{"exec", name}, args...)
	out, err := exec.CommandContext(ctx, d.bin(), full...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("docker exec %s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

// ImagePresent reports whether a ref is already in the host image cache.
//
// No container CLI, or a ref that is not there, is "not cached" rather than an
// error -- the caller falls through to the published ref and pulls.
func (d *Docker) ImagePresent(ctx context.Context, ref string) bool {
	if ref == "" {
		return false
	}
	_, err := d.capture(ctx, "image", "inspect", "-f", "{{.Id}}", ref)
	return err == nil
}

// PullImage fetches an image reference, with no timeout of its own.
//
// Deliberately not d.capture: that caps every call at d.Timeout, which is 15
// seconds, and a cold pull of a Postgres or k3s image is minutes. A pull that
// inherited it would fail as a timeout and read as a broken registry.
func (d *Docker) PullImage(ctx context.Context, ref string) error {
	if ref == "" {
		return errors.New("no image reference")
	}
	var last string
	for attempt := 1; ; attempt++ {
		var errBuf bytes.Buffer
		c := exec.CommandContext(ctx, d.bin(), "pull", ref)
		c.Stdout = io.Discard
		c.Stderr = &errBuf
		err := c.Run()
		if err == nil {
			return nil
		}
		last = strings.TrimSpace(errBuf.String())
		if last == "" {
			last = err.Error()
		}
		if attempt == PullAttempts || ctx.Err() != nil || !retryablePull(last) {
			return fmt.Errorf("docker pull %s: %s", ref, last)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("docker pull %s: %s", ref, last)
		case <-time.After(time.Duration(attempt) * PullBackoff):
		}
	}
}

// PullAttempts and PullBackoff bound the retry in PullImage. The backoff is
// linear in the attempt: a registry that just refused a connection is not
// helped by being asked again immediately, and a start should not spend
// minutes discovering that a registry is genuinely down.
const (
	PullAttempts = 3
	PullBackoff  = 2 * time.Second
)

// retryablePull reports whether a failed pull is worth repeating.
//
// A cold engine is the case this exists for. A long-lived Docker Desktop
// install has accumulated the images of every platform that machine ever ran,
// so a start pulls almost nothing; a freshly created VM -- Colima, Rancher, a
// CI runner -- holds none of them and one control-plane start fetches ten.
// Registry reads fail occasionally (ghcr.io answers on several addresses, and
// a token fetch that times out against one succeeds against the next), and a
// single such failure aborted the whole start after minutes of downloading,
// with nothing resumable and no suggestion that trying again would work.
//
// Only what is worth repeating is repeated. An image that is not there, a tag
// that does not exist, or a credential that is refused fails the same way
// however many times it is asked; retrying those makes the error slower and
// no better. So this matches the transport failures by name rather than
// retrying everything that is not recognised.
func retryablePull(msg string) bool {
	m := strings.ToLower(msg)
	for _, permanent := range []string{
		"manifest unknown", "not found", "denied", "unauthorized",
		"authentication required", "invalid reference",
	} {
		if strings.Contains(m, permanent) {
			return false
		}
	}
	for _, transient := range []string{
		"i/o timeout", "timeout exceeded", "context deadline exceeded",
		"connection refused", "connection reset", "no such host",
		"tls handshake", "eof",
		"temporary failure", "server misbehaving",
		"500 internal server error", "502 bad gateway",
		"503 service unavailable", "504 gateway timeout",
		"too many requests",
	} {
		if strings.Contains(m, transient) {
			return true
		}
	}
	return false
}

// Start starts an existing container.
func (d *Docker) Start(ctx context.Context, name string) error {
	_, err := d.capture(ctx, "start", name)
	return err
}

// NetworkAliases reports the aliases a container carries on a network.
func (d *Docker) NetworkAliases(ctx context.Context, name, network string) []string {
	format := `{{with index .NetworkSettings.Networks "` + network + `"}}{{json .Aliases}}{{end}}`
	out, err := d.capture(ctx, "inspect", name, "--format", format)
	if err != nil || out == "" || out == "null" {
		return nil
	}
	var aliases []string
	if err := json.Unmarshal([]byte(out), &aliases); err != nil {
		return nil
	}
	return aliases
}

// EnsureNetworkAlias attaches an alias to a container on a network, reporting
// whether it had to reconnect the container to do it.
//
// Docker refuses to add an alias to an existing endpoint, so this disconnects
// and reconnects. It is skipped entirely when the alias is already present, so
// a warm start does not churn the container's networking.
//
// The alias is what makes Floci's opaquely named backend reachable: the whole
// local platform addresses the database as hmd_db -- compose peers, psql, and
// cloud Helm charts running unmodified in k3s -- and aliasing restores that one
// canonical name instead of rewriting every consumer.
//
// The bool is not decoration. A reconnect gives the container a *new IP*, and a
// server that bound to the old one keeps listening there: the Gremlin server in
// hmd-img-gremlin-server binds the container's specific address, so a cold
// bootstrap left the control-plane graph listening on 172.18.0.8 while
// `global-graph` resolved to 172.18.0.7, and every client got connection
// refused. Nothing looked wrong -- `docker ps` reported it up and its own log
// showed a healthy server. Invisible on a warm platform, where the alias is
// already present and this returns at the loop above.
//
// Only the caller knows whether its container survives a restart, so the
// decision is returned rather than taken here.
func (d *Docker) EnsureNetworkAlias(ctx context.Context, name, alias, network string) (bool, error) {
	for _, existing := range d.NetworkAliases(ctx, name, network) {
		if existing == alias {
			return false, nil
		}
	}
	if _, err := d.capture(ctx, "network", "disconnect", network, name); err != nil {
		return false, fmt.Errorf("disconnecting %s from %s: %w", name, network, err)
	}
	if _, err := d.capture(ctx, "network", "connect", "--alias", alias, network, name); err != nil {
		return false, fmt.Errorf("reconnecting %s to %s as %s: %w", name, network, alias, err)
	}
	return true, nil
}

// EnsureNetworkAliases attaches every alias in want to a container on a
// network, keeping the aliases it already carries, and reports whether it had
// to reconnect.
//
// EnsureNetworkAlias reconnects with the one alias it was given, which is
// right for a database container that carries exactly one. The proxy carries
// several -- the identity provider's issuer, and every Ingress hostname a
// Floci Lambda reaches a UI or API by -- and adding one must not drop the
// rest. Docker refuses to add an alias to an existing endpoint, so this
// disconnects and reconnects with the union.
func (d *Docker) EnsureNetworkAliases(ctx context.Context, name, network string, want []string) (bool, error) {
	existing := d.NetworkAliases(ctx, name, network)
	have := make(map[string]bool, len(existing))
	for _, a := range existing {
		have[a] = true
	}
	missing := false
	for _, a := range want {
		if !have[a] {
			missing = true
			break
		}
	}
	if !missing {
		return false, nil
	}
	seen := make(map[string]bool, len(existing)+len(want))
	args := []string{"network", "connect"}
	for _, a := range append(append([]string{}, existing...), want...) {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		args = append(args, "--alias", a)
	}
	args = append(args, network, name)
	if _, err := d.capture(ctx, "network", "disconnect", network, name); err != nil {
		return false, fmt.Errorf("disconnecting %s from %s: %w", name, network, err)
	}
	if _, err := d.capture(ctx, args...); err != nil {
		return false, fmt.Errorf("reconnecting %s to %s with its aliases: %w", name, network, err)
	}
	return true, nil
}

// Restart restarts a container.
func (d *Docker) Restart(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	_, err := d.capture(ctx, "restart", name)
	return err
}

// Run executes a docker subcommand, returning stdout and stderr separately.
//
// Separate rather than combined because stdout carries data a caller parses --
// `kubectl get -o json` -- while stderr carries diagnosis. Merging them makes
// the JSON unparseable the moment anything warns.
func (d *Docker) Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error) {
	var out, errBuf bytes.Buffer
	c := exec.CommandContext(ctx, d.bin(), args...)
	c.Stdout = &out
	c.Stderr = &errBuf
	err = c.Run()
	return out.Bytes(), errBuf.Bytes(), err
}

// ContainerIP is a container's IP address on a network, or "" when it has none
// there.
//
// The result is parsed rather than trusted. A stopped container still lists the
// network, but its IPAddress is empty and Docker's template renders that as the
// literal string "invalid IP" -- which is truthy, so a caller that only checks
// for a non-empty string writes "invalid IP" into whatever it was building. The
// Python's _resolve_floci_ip has the same hole; it is simply never reached,
// because floci_container_name only ever returns running containers.
func (d *Docker) ContainerIP(ctx context.Context, name, network string) string {
	if name == "" || network == "" {
		return ""
	}
	format := `{{with index .NetworkSettings.Networks "` + network + `"}}{{.IPAddress}}{{end}}`
	out, err := d.capture(ctx, "inspect", "-f", format, name)
	if err != nil {
		return ""
	}
	return parseContainerIP(out)
}

// parseContainerIP validates docker's template output. Split out so the
// "invalid IP" case is testable without a daemon.
func parseContainerIP(out string) string {
	if net.ParseIP(out) == nil {
		return ""
	}
	return out
}

// ImageOf is the image a container was created from, or "" when it cannot be
// read.
//
// The empty result is deliberately ambiguous between "no such container" and
// "could not inspect it", and callers must treat it as unknown rather than as
// staleness: recreating a k3s cluster over a transient docker hiccup would drop
// its datastore and every Helm release on it.
func (d *Docker) ImageOf(ctx context.Context, name string) string {
	if name == "" {
		return ""
	}
	out, err := d.capture(ctx, "inspect", "-f", "{{.Config.Image}}", name)
	if err != nil {
		return ""
	}
	return out
}

// ContainerEnv is the environment a container was created with.
func (d *Docker) ContainerEnv(ctx context.Context, name string) map[string]string {
	if name == "" {
		return nil
	}
	out, err := d.capture(ctx, "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", name)
	if err != nil {
		return nil
	}
	return envMap(out)
}

// DefaultBridgeNetwork is Docker's built-in bridge, the one every container
// lands on when none is named at create time.
const DefaultBridgeNetwork = "bridge"

// State is a container's runtime state as `docker inspect` reports it.
type State struct {
	Running    bool   `json:"Running"`
	Status     string `json:"Status"`
	ExitCode   int    `json:"ExitCode"`
	Error      string `json:"Error"`
	OOMKilled  bool   `json:"OOMKilled"`
	StartedAt  string `json:"StartedAt"`
	FinishedAt string `json:"FinishedAt"`
}

// InspectState reads a container's State.
//
// The second return distinguishes "could not ask" from "answered", and callers
// must honour it. It is the same doctrine as ImageOf's empty result: a docker
// hiccup read as "the cluster died" would fail a start that was fine.
func (d *Docker) InspectState(ctx context.Context, name string) (State, bool) {
	if name == "" {
		return State{}, false
	}
	out, err := d.capture(ctx, "inspect", "-f", "{{json .State}}", name)
	if err != nil {
		return State{}, false
	}
	return parseState(out)
}

// parseState decodes `docker inspect -f '{{json .State}}'`. Split out so the
// malformed case is testable without a daemon.
func parseState(out string) (State, bool) {
	var s State
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		return State{}, false
	}
	if s.Status == "" {
		return State{}, false
	}
	return s, true
}

// ContainerNetworks lists the networks a container is attached to.
func (d *Docker) ContainerNetworks(ctx context.Context, name string) []string {
	if name == "" {
		return nil
	}
	out, err := d.capture(ctx, "inspect", "-f",
		"{{range $net, $_ := .NetworkSettings.Networks}}{{println $net}}{{end}}", name)
	if err != nil {
		return nil
	}
	var nets []string
	for _, line := range strings.Split(out, "\n") {
		if n := strings.TrimSpace(line); n != "" {
			nets = append(nets, n)
		}
	}
	return nets
}

// DetachFromDefaultBridge disconnects a container from Docker's default bridge,
// leaving keep as its attachment.
//
// Floci's EKS spawner attaches the k3s container to `bridge` in addition to the
// network FLOCI_SERVICES_EKS_DOCKER_NETWORK names -- every other container it
// spawns (RDS, Neptune, Lambda) gets the configured network alone. k3s then
// takes eth0, the default bridge, for its node IP, and the default bridge hands
// out addresses by start order. So the node IP churns between runs while the
// datastore in /var/lib/rancher/k3s keeps the previous one, and whatever reads
// the stored address finds no interface holding it.
//
// Idempotent in the way EnsureNetworkAlias is: a container that is not on the
// bridge is left completely alone, so a restart does not churn networking. A
// container whose *only* network is the bridge is refused rather than stranded
// -- disconnecting it would leave it unreachable, and that shape means keep is
// not the network the caller believes it is.
func (d *Docker) DetachFromDefaultBridge(ctx context.Context, name, keep string) (bool, error) {
	if name == "" || keep == "" {
		return false, nil
	}
	var onBridge, onKeep bool
	for _, n := range d.ContainerNetworks(ctx, name) {
		switch n {
		case DefaultBridgeNetwork:
			onBridge = true
		case keep:
			onKeep = true
		}
	}
	if !onBridge {
		return false, nil
	}
	if !onKeep {
		return false, fmt.Errorf("%s is attached only to Docker's default bridge, not to %s; "+
			"leaving it alone rather than disconnecting its only network", name, keep)
	}
	if _, err := d.capture(ctx, "network", "disconnect", DefaultBridgeNetwork, name); err != nil {
		// Losing the race with another disconnect is the outcome we wanted.
		if strings.Contains(err.Error(), "is not connected to network") {
			return false, nil
		}
		return false, fmt.Errorf("disconnecting %s from Docker's default bridge: %w", name, err)
	}
	return true, nil
}

// Stop stops a running container.
func (d *Docker) Stop(ctx context.Context, name string) error {
	_, err := d.capture(ctx, "stop", name)
	return err
}

// Logs is the tail of a container's output, best effort.
//
// Combined, because a crash-loop's cause lands on whichever stream the process
// chose and the caller only wants to quote it back. An unreadable log is "",
// never an error: it is diagnosis, and failing to gather it must not change
// what the caller decides.
func (d *Docker) Logs(ctx context.Context, name string, lines int) string {
	if name == "" || lines <= 0 {
		return ""
	}
	stdout, stderr, err := d.Run(ctx, "logs", "--tail", strconv.Itoa(lines), name)
	if err != nil && len(stdout) == 0 && len(stderr) == 0 {
		return ""
	}
	return strings.TrimSpace(strings.TrimSpace(string(stdout)) + "\n" + strings.TrimSpace(string(stderr)))
}

// RemoveContainer force-removes a container. One that is not there is not an
// error: the caller wanted it gone.
func (d *Docker) RemoveContainer(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	if _, err := d.capture(ctx, "rm", "-f", name); err != nil {
		if strings.Contains(err.Error(), "No such container") {
			return nil
		}
		return fmt.Errorf("removing the container %s: %w", name, err)
	}
	return nil
}

// RemoveVolumes force-removes volumes, skipping any that are not there.
func (d *Docker) RemoveVolumes(ctx context.Context, names ...string) error {
	var failed []string
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, err := d.capture(ctx, "volume", "rm", "-f", name); err != nil {
			if strings.Contains(err.Error(), "no such volume") ||
				strings.Contains(err.Error(), "No such volume") {
				continue
			}
			// Recorded and carried on, never returned early. The caller that
			// matters is the purge's end-of-run sweep, whose whole purpose is
			// the volumes an earlier purge could not reach -- and a sweep that
			// stops at the first volume it cannot reach is how they accumulate
			// in the first place. One in-use floci-ecr-registry-data left two
			// floci-rds-db-* volumes behind on the first real run of this.
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not remove %d of %d volume(s): %s",
			len(failed), len(names), strings.Join(failed, "; "))
	}
	return nil
}

// RemoveNetwork removes a Docker network. One that is not there, or one still
// holding endpoints, is not an error: a purge reports what it could not remove
// rather than stopping on it.
func (d *Docker) RemoveNetwork(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	if _, err := d.capture(ctx, "network", "rm", name); err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "No such network") {
			return nil
		}
		return fmt.Errorf("removing the network %s: %w", name, err)
	}
	return nil
}

// VolumesMatching lists volumes whose name contains prefix.
//
// Name matching, because Floci's spawned volumes carry no labels tying them to
// an account -- which is why the Python sweeps `floci-rds-` by name too. An
// unreadable answer is an empty list, never a partial one presented as
// complete.
//
// Every match, including volumes another platform is using. A caller that
// intends to *delete* what it finds must use DanglingVolumesMatching instead --
// see the scar recorded there.
func (d *Docker) VolumesMatching(ctx context.Context, prefix string) []string {
	return d.volumesMatching(ctx, prefix, false)
}

// DanglingVolumesMatching lists volumes whose name contains prefix and which no
// container references.
//
// This is the only safe basis for a sweep that deletes. Floci volume names are
// global to the Docker daemon and carry nothing identifying the HMD_HOME that
// created them, while the network, compose project and Floci data directory are
// all namespaced -- so a name-only sweep from one HMD_HOME matches every other
// one's volumes too. `env purge` did exactly that and destroyed a working
// platform's control-plane database, its deployment graph and its clusters'
// datastores, from a throwaway HMD_HOME that had nothing to do with it. The
// sweep's own comment said "by this point every account is going anyway", which
// is true within one Floci and false across homes.
//
// Dangling is the right discriminator rather than a cleverer one: a volume no
// container references is a genuine leftover, which is what the sweep was
// written to collect, and a volume some other platform's container still holds
// -- stopped or running -- is that platform's. The purging home's own volumes
// are dangling by the time this runs, because its containers were removed
// first, so nothing it should collect is missed.
func (d *Docker) DanglingVolumesMatching(ctx context.Context, prefix string) []string {
	return d.volumesMatching(ctx, prefix, true)
}

func (d *Docker) volumesMatching(ctx context.Context, prefix string, danglingOnly bool) []string {
	if prefix == "" {
		return nil
	}
	args := []string{"volume", "ls", "--filter", "name=" + prefix}
	if danglingOnly {
		args = append(args, "--filter", "dangling=true")
	}
	args = append(args, "--format", "{{.Name}}")
	out, err := d.capture(ctx, args...)
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Fields(out) {
		if line != "" {
			names = append(names, line)
		}
	}
	return names
}

// ContainersWithLabel lists every container, running or not, carrying a label.
//
// This is how a purge finds an interrupted projectbuilder: it exits with a
// Docker-generated name and nothing else ties it to the environment whose
// deploy started it, so the label the runner sets is the only handle.
func (d *Docker) ContainersWithLabel(ctx context.Context, key, value string) []string {
	return d.ContainersWithLabelOnNetwork(ctx, key, value, "")
}

// ContainersWithLabelOnNetwork narrows that to one network.
//
// Which is what makes it safe to *remove* what it finds. Floci's spawned
// containers carry an account label, but accounts are allocated per registry
// and every HMD_HOME starts at 000000000001 -- so the label says nothing about
// which platform a container belongs to. The network does: it is
// `neuronsphere_default-<HMD_HOME hash>`, namespaced like the compose project
// and the Floci data directory, and a stopped container keeps its attachment.
//
// A daemon-wide sweep by label alone removed another platform's containers,
// which then dangled its volumes and let the volume sweep take those too. The
// dangling check on that sweep is necessary and was not sufficient, because
// this ran first and created exactly the condition it tests for.
func (d *Docker) ContainersWithLabelOnNetwork(ctx context.Context, key, value, network string) []string {
	if key == "" {
		return nil
	}
	filter := key
	if value != "" {
		filter = key + "=" + value
	}
	args := []string{"ps", "-a", "--filter", "label=" + filter}
	if network != "" {
		args = append(args, "--filter", "network="+network)
	}
	args = append(args, "--format", "{{.Names}}")
	out, err := d.capture(ctx, args...)
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Fields(out) {
		if line != "" {
			names = append(names, line)
		}
	}
	return names
}

// ImageEnv is the environment an image declares.
//
// `image inspect` rather than the plain `inspect` ContainerEnv uses: the two
// namespaces overlap, and plain inspect resolves a container first, so an image
// sharing a name with a container would be read as the container. That matters
// here because the caller is comparing an image's declared version against a
// container's -- reading one when it meant the other would compare a thing to
// itself and find no difference.
func (d *Docker) ImageEnv(ctx context.Context, ref string) map[string]string {
	if ref == "" {
		return nil
	}
	out, err := d.capture(ctx, "image", "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", ref)
	if err != nil {
		return nil
	}
	return envMap(out)
}

// VolumeUserImage is the image of a container that mounts a volume, or "" when
// none does.
//
// That container is what initialised the volume's contents, which makes it both
// the right image to read those contents with -- it is present locally by
// definition, so reading costs no pull -- and the most useful thing to name in
// an error: "pin this back" is a cheaper remedy to offer than "discard your
// data".
func (d *Docker) VolumeUserImage(ctx context.Context, volume string) string {
	if volume == "" {
		return ""
	}
	out, err := d.capture(ctx, "ps", "-a", "--filter", "volume="+volume, "--format", "{{.Image}}")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// envMap parses `docker inspect`'s rendered KEY=VALUE lines.
func envMap(out string) map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if found && key != "" {
			env[key] = value
		}
	}
	return env
}
