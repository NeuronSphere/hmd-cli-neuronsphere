package floci

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dockercreds"
)

// PrivateImageRegistry is the registry the platform's private images are
// published to, and so the one whose docker login nsctl hands to k3s.
const PrivateImageRegistry = "ghcr.io"

// K3sRegistriesPath is where k3s reads registry configuration from at boot.
const K3sRegistriesPath = "/etc/rancher/k3s/registries.yaml"

// K3sRegistryDocker is what SyncK3sRegistryAuth needs from the engine.
type K3sRegistryDocker interface {
	Exec(ctx context.Context, name string, args ...string) ([]byte, error)
	Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error)
}

// SyncK3sRegistryAuth gives the cluster's containerd the host's registry
// credentials, and reports whether it had to restart k3s to do it.
//
// The k3s container is Floci's -- spawned through the EKS API with no knob
// for mounts -- and k3s pulls with containerd, which reads nothing of the
// docker CLI's login. Without this every private image (ghcr.io packages are
// private by default) sat in ImagePullBackOff, and the only fix was a docker
// login nothing passed on. k3s reads registries.yaml only at boot, so a
// changed file costs one restart; an unchanged one costs nothing, which is
// every warm start.
//
// The file is staged owner-only under stageDir and copied in, so the secret
// is never on a command line, and the staged copy is removed.
func SyncK3sRegistryAuth(ctx context.Context, d K3sRegistryDocker, name string,
	creds map[string]dockercreds.Credential, stageDir string) (bool, error) {

	if name == "" || len(creds) == 0 {
		return false, nil
	}
	want := renderRegistries(creds)
	if have, err := d.Exec(ctx, name, "cat", K3sRegistriesPath); err == nil && string(have) == want {
		return false, nil
	}

	staged, err := os.CreateTemp(stageDir, "registries-*.yaml")
	if err != nil {
		return false, fmt.Errorf("staging the cluster's registry credentials: %w", err)
	}
	defer os.Remove(staged.Name())
	if err := staged.Chmod(0o600); err == nil {
		_, err = staged.WriteString(want)
	}
	if cerr := staged.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, fmt.Errorf("staging the cluster's registry credentials: %w", err)
	}

	if _, stderr, err := d.Run(ctx, "cp", staged.Name(), name+":"+K3sRegistriesPath); err != nil {
		return false, fmt.Errorf("copying registry credentials into %s: %s", name, firstNonEmpty(strings.TrimSpace(string(stderr)), err.Error()))
	}
	if _, stderr, err := d.Run(ctx, "restart", name); err != nil {
		return true, fmt.Errorf("restarting %s to load its registry credentials: %s", name, firstNonEmpty(strings.TrimSpace(string(stderr)), err.Error()))
	}
	return true, nil
}

// renderRegistries writes registries.yaml deterministically, so an unchanged
// credential compares equal. JSON is valid YAML, and quoting every value
// through the JSON encoder means no secret can break the document.
func renderRegistries(creds map[string]dockercreds.Credential) string {
	hosts := make([]string, 0, len(creds))
	for h := range creds {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	var b strings.Builder
	b.WriteString("# Written by nsctl from the docker CLI's login. Do not edit: it is rewritten on start.\nconfigs:\n")
	for _, h := range hosts {
		c := creds[h]
		host, _ := json.Marshal(h)
		user, _ := json.Marshal(c.Username)
		secret, _ := json.Marshal(c.Secret)
		fmt.Fprintf(&b, "  %s:\n    auth:\n      username: %s\n      password: %s\n", host, user, secret)
	}
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
