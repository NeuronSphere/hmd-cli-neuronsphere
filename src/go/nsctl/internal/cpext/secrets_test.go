package cpext

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/keyring"
)

// stubResolver answers by service name, so the probe order and the delivery
// logic are testable without a host keychain.
type stubResolver struct {
	values map[string]string
	asked  [][]string
	err    error
}

func (s *stubResolver) ResolveAny(_ context.Context, services []string, username string) (string, error) {
	s.asked = append(s.asked, services)
	if s.err != nil {
		return "", s.err
	}
	for _, svc := range services {
		if v, ok := s.values[svc+"|"+username]; ok {
			return v, nil
		}
	}
	return "", keyring.ErrNotFound
}

const secretCompose = `
services:
  server:
    image: thing:1
    tmpfs:
      - /run/ns-secrets
    networks: [neuronsphere_default]
  sidecar:
    image: thing:1
    networks: [neuronsphere_default]
`

func credentialFixture(t *testing.T, config string) Options {
	t.Helper()
	home, repos := t.TempDir(), t.TempDir()
	repoClass(t, repos, "hmd-inf-local-registry", "0.1.4", secretCompose)
	manifestFile(t, home, `
version: 1
name: control-plane
repos:
  - instance_name: package-registry
    repo_class_name: hmd-inf-local-registry
    instance_configuration:
`+config)
	return Options{Home: home, RepoHome: repos, ProjectName: projectName,
		Lookup:   env(map[string]string{"HMD_REPO_HOME": repos, "USER": "me"}),
		Networks: platformNetworks}
}

const oneCredential = `      credentials:
        - name: hmd-upstream
          keyring_service: "uv:hmdlabs.jfrog.io"
          keyring_user: "${USER}"
`

func TestCredentialsAreReadFromTheManifest(t *testing.T) {
	t.Parallel()

	exts, err := Resolve(credentialFixture(t, oneCredential))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	e := exts[0]
	if e.Failed() {
		t.Fatalf("resolve failed: %v", e.Err)
	}
	if len(e.Credentials) != 1 {
		t.Fatalf("credentials = %+v", e.Credentials)
	}
	c := e.Credentials[0]
	if c.Service != "uv:hmdlabs.jfrog.io" {
		t.Errorf("service = %q", c.Service)
	}
	// ${USER} is expanded, or the account would be the literal string.
	if c.User != "me" {
		t.Errorf("user = %q, want the expanded ${USER}", c.User)
	}
	if c.Deliver != DeliverFile {
		t.Errorf("deliver = %q, want the tmpfs default", c.Deliver)
	}
}

// A manifest is a file a developer edits and may commit. A secret in one is a
// secret in a git history, so it is refused rather than used.
func TestALiteralPasswordIsRefused(t *testing.T) {
	t.Parallel()

	exts, _ := Resolve(credentialFixture(t, `      credentials:
        - name: hmd
          keyring_service: "uv:h"
          password: "s3cr3t"
`))
	assertFailure(t, exts, "never holds one")
}

func TestACredentialMustNameSomethingToLookUp(t *testing.T) {
	t.Parallel()

	exts, _ := Resolve(credentialFixture(t, `      credentials:
        - name: hmd
`))
	assertFailure(t, exts, "nothing to look up")
}

func TestUnknownDeliveryIsRefused(t *testing.T) {
	t.Parallel()

	exts, _ := Resolve(credentialFixture(t, `      credentials:
        - name: hmd
          keyring_service: "uv:h"
          deliver: telepathy
`))
	assertFailure(t, exts, "unknown deliver")
}

func TestDuplicateCredentialNamesAreRefused(t *testing.T) {
	t.Parallel()

	exts, _ := Resolve(credentialFixture(t, `      credentials:
        - name: hmd
          keyring_service: "uv:a"
        - name: hmd
          keyring_service: "uv:b"
`))
	assertFailure(t, exts, "duplicate name")
}

// An explicit service is probed first, then the URL's candidates -- so a
// manifest can name the service it expects and still fall back.
func TestCredentialServicesAreProbedInOrder(t *testing.T) {
	t.Parallel()

	c := Credential{Service: "uv:explicit", URL: "https://h.example/simple"}
	got := c.Services()
	if len(got) == 0 || got[0] != "uv:explicit" {
		t.Fatalf("Services = %q, want the explicit one first", got)
	}
	if got[1] != "uv:h.example" {
		t.Errorf("Services = %q, want the URL candidates after it", got)
	}
}

// A credential that will not resolve is recorded on itself. Failing the whole
// extension would take the cached public index down with the private upstream.
func TestAnUnresolvedCredentialDoesNotFailTheExtension(t *testing.T) {
	t.Parallel()

	exts, _ := Resolve(credentialFixture(t, oneCredential))
	e := &exts[0]
	e.ResolveCredentials(context.Background(), &stubResolver{}, "me")

	if e.Failed() {
		t.Fatalf("the extension failed for an unresolved credential: %v", e.Err)
	}
	if e.Credentials[0].Resolved {
		t.Fatal("a credential resolved against an empty keychain")
	}
	// The report has to say which resolved and which did not, or a 401 from a
	// cache is indistinguishable from an empty index.
	report := strings.Join(CredentialReport(exts), "\n")
	if !strings.Contains(report, "package-registry/hmd-upstream did not resolve") {
		t.Errorf("report = %q", report)
	}
	if !strings.Contains(report, "uv:hmdlabs.jfrog.io") {
		t.Errorf("report %q does not say where it looked", report)
	}
}

func TestResolvedCredentialsAreReportedWithoutTheValue(t *testing.T) {
	t.Parallel()

	exts, _ := Resolve(credentialFixture(t, oneCredential))
	e := &exts[0]
	e.ResolveCredentials(context.Background(),
		&stubResolver{values: map[string]string{"uv:hmdlabs.jfrog.io|me": "s3cr3t"}}, "me")

	if !e.Credentials[0].Resolved {
		t.Fatal("the credential did not resolve")
	}
	report := strings.Join(CredentialReport(exts), "\n")
	if strings.Contains(report, "s3cr3t") {
		t.Fatalf("the report printed the value: %q", report)
	}
	if !strings.Contains(report, "resolved from uv:hmdlabs.jfrog.io") {
		t.Errorf("report = %q", report)
	}
}

// -- delivery --------------------------------------------------------------

type fakeWriter struct {
	written map[string][]byte
	err     error
}

func (f *fakeWriter) WriteInto(_ context.Context, name, path string, data []byte) error {
	if f.err != nil {
		return f.err
	}
	if f.written == nil {
		f.written = map[string][]byte{}
	}
	f.written[name+":"+path] = data
	return nil
}

func resolvedFixture(t *testing.T, config string) (*Extension, *fakeWriter) {
	t.Helper()
	exts, err := Resolve(credentialFixture(t, config))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if exts[0].Failed() {
		t.Fatalf("resolve failed: %v", exts[0].Err)
	}
	e := &exts[0]
	e.ResolveCredentials(context.Background(),
		&stubResolver{values: map[string]string{
			"uv:hmdlabs.jfrog.io|me": "s3cr3t",
			"uv:h|me":                "s3cr3t",
		}}, "me")
	return e, &fakeWriter{}
}

func TestSecretsAreDeliveredIntoTheTmpfs(t *testing.T) {
	t.Parallel()

	e, w := resolvedFixture(t, oneCredential)
	if problems := DeliverSecrets(context.Background(), w, *e, projectName); len(problems) > 0 {
		t.Fatalf("DeliverSecrets: %v", problems)
	}

	key := projectName + "-package-registry-server-1:/run/ns-secrets/hmd-upstream"
	value, ok := w.written[key]
	if !ok {
		t.Fatalf("nothing delivered to %s; got %v", key, keysOf(w.written))
	}
	if string(value) != "s3cr3t" {
		t.Errorf("delivered %q", value)
	}
	// Only into the service that declares the tmpfs. A container with no
	// in-memory mount would take the write to its writable layer, which is
	// disk.
	for k := range w.written {
		if strings.Contains(k, "sidecar") {
			t.Errorf("delivered to a service with no tmpfs: %s", k)
		}
	}
}

// A credential that resolved and was delivered nowhere is the silent half of
// an unresolved one, so it is reported rather than skipped.
func TestDeliveryWithNoTmpfsIsReported(t *testing.T) {
	t.Parallel()

	e, w := resolvedFixture(t, oneCredential)
	for i := range e.Services {
		e.Services[i].Tmpfs = nil
	}
	problems := DeliverSecrets(context.Background(), w, *e, projectName)
	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "nowhere to deliver") {
		t.Fatalf("problems = %v", problems)
	}
	if len(w.written) != 0 {
		t.Errorf("something was delivered anyway: %v", keysOf(w.written))
	}
}

func TestDeliveryFailureIsScrubbed(t *testing.T) {
	t.Parallel()

	e, _ := resolvedFixture(t, oneCredential)
	w := &fakeWriter{err: errors.New("no route to https://user:s3cr3t@h.example/x")}
	problems := DeliverSecrets(context.Background(), w, *e, projectName)
	if len(problems) != 1 {
		t.Fatalf("problems = %v", problems)
	}
	if strings.Contains(problems[0].Error(), "s3cr3t") {
		t.Fatalf("an error path leaked a credential: %v", problems[0])
	}
}

// The documented weaker option: visible in `docker inspect`, and only for a
// service that cannot read its configuration from a file.
func TestEnvDeliveryPutsTheValueInTheServiceEnvironment(t *testing.T) {
	t.Parallel()

	e, w := resolvedFixture(t, `      credentials:
        - name: hmd-upstream
          keyring_service: "uv:hmdlabs.jfrog.io"
          deliver: env
`)
	applyEnvSecrets(e)
	for _, s := range e.Services {
		if s.Environment["NS_SECRET_HMD_UPSTREAM"] != "s3cr3t" {
			t.Errorf("%s environment = %v", s.Key, s.Environment)
		}
	}
	// And nothing is written into a container.
	if problems := DeliverSecrets(context.Background(), w, *e, projectName); len(problems) != 0 {
		t.Errorf("env delivery also wrote a file: %v", problems)
	}
	if len(w.written) != 0 {
		t.Errorf("env delivery also wrote a file: %v", keysOf(w.written))
	}
}

// An env-delivered credential reaches the config hash, which is the one good
// thing about that path: a rotated credential recreates the container rather
// than being ignored until something else restarts it.
func TestEnvDeliveryChangesTheConfigHash(t *testing.T) {
	t.Parallel()

	e, _ := resolvedFixture(t, `      credentials:
        - name: hmd-upstream
          keyring_service: "uv:hmdlabs.jfrog.io"
          deliver: env
`)
	p := Project([]Extension{*e}, Options{ProjectName: projectName, Networks: platformNetworks})
	before := compose.ConfigHash(p, p.Services[0])

	applyEnvSecrets(e)
	after := Project([]Extension{*e}, Options{ProjectName: projectName, Networks: platformNetworks})
	if compose.ConfigHash(after, after.Services[0]) == before {
		t.Error("the hash did not change, so a rotated credential would never take effect")
	}
}

// The acceptance gate SPEC009 states: after resolution and delivery, a
// recursive search for the secret across $HMD_HOME finds nothing.
func TestNoCredentialIsWrittenUnderHMDHome(t *testing.T) {
	t.Parallel()

	opts := credentialFixture(t, oneCredential)
	exts, err := Resolve(opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	e := &exts[0]
	e.ResolveCredentials(context.Background(),
		&stubResolver{values: map[string]string{"uv:hmdlabs.jfrog.io|me": "s3cr3t"}}, "me")
	if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if problems := DeliverSecrets(context.Background(), &fakeWriter{}, *e, projectName); len(problems) > 0 {
		t.Fatalf("DeliverSecrets: %v", problems)
	}

	if hits := grepTree(t, opts.Home, "s3cr3t"); len(hits) > 0 {
		t.Errorf("the credential was written under $HMD_HOME: %v", hits)
	}
	// And not into the repo tree either, which is bind-mounted into nothing
	// here but is a developer's checkout.
	if hits := grepTree(t, opts.RepoHome, "s3cr3t"); len(hits) > 0 {
		t.Errorf("the credential was written into the repo tree: %v", hits)
	}
}

// -- helpers ---------------------------------------------------------------

func grepTree(t *testing.T, root, needle string) []string {
	t.Helper()
	var hits []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if bytes.Contains(data, []byte(needle)) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return hits
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
