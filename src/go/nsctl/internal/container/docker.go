package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
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

// EnsureNetworkAlias attaches an alias to a container on a network.
//
// Docker refuses to add an alias to an existing endpoint, so this disconnects
// and reconnects. That is safe because it is skipped entirely when the alias is
// already present, so a restart does not churn the container's networking.
//
// The alias is what makes Floci's opaquely named backend reachable: the whole
// local platform addresses the database as hmd_db -- compose peers, psql, and
// cloud Helm charts running unmodified in k3s -- and aliasing restores that one
// canonical name instead of rewriting every consumer.
func (d *Docker) EnsureNetworkAlias(ctx context.Context, name, alias, network string) error {
	for _, existing := range d.NetworkAliases(ctx, name, network) {
		if existing == alias {
			return nil
		}
	}
	if _, err := d.capture(ctx, "network", "disconnect", network, name); err != nil {
		return fmt.Errorf("disconnecting %s from %s: %w", name, network, err)
	}
	if _, err := d.capture(ctx, "network", "connect", "--alias", alias, network, name); err != nil {
		return fmt.Errorf("reconnecting %s to %s as %s: %w", name, network, alias, err)
	}
	return nil
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
