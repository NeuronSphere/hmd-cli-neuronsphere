package container

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	// One resource, one backing container; take the first line if Floci ever
	// leaves a second behind.
	line := strings.SplitN(out, "\n", 2)[0]
	name, state, _ := strings.Cut(line, "\t")
	return strings.TrimSpace(name), strings.TrimSpace(state) == "running"
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
