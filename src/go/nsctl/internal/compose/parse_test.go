package compose

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bundled"
)

// controlPlaneEnv is what environments.export_control_plane_compose_env sets
// before compose runs, plus HMD_HOME.
func controlPlaneEnv() map[string]string {
	return map[string]string{
		"HMD_HOME":                    "/Users/aburg/hmdtr1",
		"NEURONSPHERE_DOCKER_NETWORK": "neuronsphere_default-57aa833c",
		"HMD_DEPLOYMENT_GUI_IMAGE":    "ghcr.io/hmdlabs/hmd-app-neuronsphere-core:0.1.74",
	}
}

func parseControlPlane(t *testing.T, env map[string]string) *Project {
	t.Helper()
	data, err := bundled.Read(bundled.ControlPlaneComposeFile)
	if err != nil {
		t.Fatalf("reading the bundled compose file: %v", err)
	}
	p, err := Parse(data, "local_neuronsphere-57aa833c", fakeEnv(env))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

func service(t *testing.T, p *Project, key string) Service {
	t.Helper()
	for _, s := range p.Services {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("no service %q in %v", key, p.Services)
	return Service{}
}

// The golden test: the real bundled file, parsed, asserted service by service.
// A mis-parse here fails at container-create time otherwise, well away from the
// cause.
func TestParseTheBundledControlPlaneFile(t *testing.T) {
	t.Parallel()

	p := parseControlPlane(t, controlPlaneEnv())

	if len(p.Services) != 4 {
		t.Fatalf("got %d services, want 4 (proxy, floci, deployment-gui, authd): %v", len(p.Services), p.Services)
	}
	// Sorted, so output and tests do not depend on map iteration order.
	want := []string{"authd", "deployment-gui", "floci", "proxy"}
	for i, w := range want {
		if p.Services[i].Key != w {
			t.Errorf("service %d = %q, want %q (services must be in a deterministic order)", i, p.Services[i].Key, w)
		}
	}

	if n, ok := p.Networks["neuronsphere_default"]; !ok {
		t.Error("the neuronsphere_default network is missing")
	} else {
		if !n.External {
			t.Error("the network must be external: nsctl creates it before starting anything")
		}
		if n.Name != "neuronsphere_default-57aa833c" {
			t.Errorf("network name = %q, want the interpolated per-HMD_HOME name", n.Name)
		}
	}
}

// hmd_proxy is the only container in the whole local stack that publishes host
// ports. Everything else is reached through it, which is what lets several
// environments coexist on one machine.
func TestParseProxyPublishesTheWholeBand(t *testing.T) {
	t.Parallel()

	s := service(t, parseControlPlane(t, controlPlaneEnv()), "proxy")

	if s.ContainerName != "hmd_proxy" {
		t.Errorf("container name = %q, want hmd_proxy", s.ContainerName)
	}
	if s.Image != "nginx:stable-alpine" {
		t.Errorf("image = %q", s.Image)
	}
	if s.Restart != "unless-stopped" {
		t.Errorf("restart = %q", s.Restart)
	}

	got := map[string]bool{}
	total := 0
	for _, port := range s.Ports {
		got[portKey(port)] = true
		total += port.Count()
	}
	for _, want := range []string{"80-80:80-80", "4566-4566:4566-4566", "18080-18080:18080-18080", "19000-19079:19000-19079"} {
		if !got[want] {
			t.Errorf("proxy does not publish %s; got %v", want, got)
		}
	}
	// 3 singles plus the 80-wide environment band.
	if total != 83 {
		t.Errorf("proxy publishes %d host ports, want 83", total)
	}

	// The route fragments are a directory bind so nginx_router can add and
	// remove listeners at runtime with a reload, no container restart.
	wantMounts := map[string]Mount{
		"/etc/nginx/nginx.conf": {Source: "/Users/aburg/hmdtr1/.cache/nginx/neuronsphere.conf", Target: "/etc/nginx/nginx.conf", ReadOnly: true},
		"/etc/nginx/ns":         {Source: "/Users/aburg/hmdtr1/.cache/nginx", Target: "/etc/nginx/ns", ReadOnly: true},
	}
	if len(s.Volumes) != len(wantMounts) {
		t.Fatalf("proxy has %d mounts, want %d: %v", len(s.Volumes), len(wantMounts), s.Volumes)
	}
	for _, m := range s.Volumes {
		w, ok := wantMounts[m.Target]
		if !ok {
			t.Errorf("unexpected mount at %s", m.Target)
			continue
		}
		if m != w {
			t.Errorf("mount %s = %+v, want %+v", m.Target, m, w)
		}
	}
}

// Floci publishes nothing -- it is reached through hmd_proxy's :4566 stream --
// and its aliases matter: Floci bakes the `neuronsphere` name into the invoke
// and endpoint URLs it hands back.
func TestParseFlociAliasesAndHealthcheck(t *testing.T) {
	t.Parallel()

	s := service(t, parseControlPlane(t, controlPlaneEnv()), "floci")

	if s.ContainerName != "floci" {
		t.Errorf("container name = %q", s.ContainerName)
	}
	if len(s.Ports) != 0 {
		t.Errorf("floci publishes %v; every published port on this machine belongs to hmd_proxy", s.Ports)
	}

	if len(s.Networks) != 1 {
		t.Fatalf("floci networks = %v, want one", s.Networks)
	}
	aliases := map[string]bool{}
	for _, a := range s.Networks[0].Aliases {
		aliases[a] = true
	}
	for _, want := range []string{"neuronsphere", "neuronsphere-workload"} {
		if !aliases[want] {
			t.Errorf("floci is missing the %q alias; got %v", want, s.Networks[0].Aliases)
		}
	}

	if s.Healthcheck == nil {
		t.Fatal("floci has no healthcheck")
	}
	wantTest := []string{"CMD", "curl", "-sf", "http://localhost:4566/_floci/health"}
	if len(s.Healthcheck.Test) != len(wantTest) {
		t.Fatalf("healthcheck test = %v, want %v", s.Healthcheck.Test, wantTest)
	}
	for i := range wantTest {
		if s.Healthcheck.Test[i] != wantTest[i] {
			t.Errorf("healthcheck test[%d] = %q, want %q", i, s.Healthcheck.Test[i], wantTest[i])
		}
	}
	if s.Healthcheck.Interval != 10*time.Second || s.Healthcheck.Timeout != 5*time.Second ||
		s.Healthcheck.StartPeriod != 30*time.Second || s.Healthcheck.Retries != 20 {
		t.Errorf("healthcheck timings = %+v", s.Healthcheck)
	}

	// The image references Floci recreates its backend containers from must
	// come out fully interpolated -- a ${...} left in one is handed to Docker
	// verbatim and fails as an invalid reference.
	//
	// Which registry and version they name is asserted in
	// internal/controlplane's TestComposeImageDefaultsNameThePublishingRegistry,
	// not here: this package's subject is interpolation, and pinning the
	// current defaults in two files means every pin move has to be made twice.
	for _, key := range []string{
		"FLOCI_SERVICES_RDS_DEFAULT_POSTGRES_IMAGE",
		"FLOCI_SERVICES_NEPTUNE_DEFAULT_IMAGE",
		"FLOCI_SERVICES_EKS_DEFAULT_IMAGE",
	} {
		got := s.Environment[key]
		if got == "" || strings.Contains(got, "${") {
			t.Errorf("%s = %q, want a fully interpolated reference", key, got)
		}
	}
	// The k3s line is the nested-default case: ${A:-${B:-literal}} with neither
	// A nor B set has to fall through to the innermost literal, which is the
	// only place in this file that shape appears.
	if got, want := s.Environment["FLOCI_SERVICES_EKS_DEFAULT_IMAGE"], "/hmd-img-k3s-floci:0.3.4"; !strings.HasSuffix(got, want) {
		t.Errorf("k3s wrapper image = %q, want the nested default to end %q", got, want)
	}
	if got, want := s.Environment["FLOCI_HOSTNAME"], "neuronsphere"; got != want {
		t.Errorf("FLOCI_HOSTNAME = %q, want %q", got, want)
	}
	if got, want := s.Environment["FLOCI_SERVICES_DOCKER_NETWORK"], "neuronsphere_default-57aa833c"; got != want {
		t.Errorf("Floci docker network = %q, want the interpolated name", got)
	}
	if got, want := s.Environment["FLOCI_DEFAULT_ACCOUNT_ID"], "000000000000"; got != want {
		t.Errorf("default account = %q, want %q", got, want)
	}
}

// The GUI is profile-gated: that is what
// HMD_LOCAL_NEURONSPHERE_ENABLE_GUI=false turns off.
func TestParseDeploymentGUIProfileAndCommand(t *testing.T) {
	t.Parallel()

	s := service(t, parseControlPlane(t, controlPlaneEnv()), "deployment-gui")

	if len(s.Profiles) != 1 || s.Profiles[0] != "deployment-gui" {
		t.Errorf("profiles = %v, want [deployment-gui]", s.Profiles)
	}
	if s.EnabledBy(map[string]bool{}) {
		t.Error("a profiled service must not run with no active profiles")
	}
	if !s.EnabledBy(map[string]bool{"deployment-gui": true}) {
		t.Error("the service must run when its profile is active")
	}
	if s.Image != "ghcr.io/hmdlabs/hmd-app-neuronsphere-core:0.1.74" {
		t.Errorf("image = %q, want the value export_control_plane_compose_env pins", s.Image)
	}

	// The migrate-retry loop: the GUI's database is an RDS instance created
	// after the containers start, so the command waits rather than depends_on.
	if len(s.Command) != 3 || s.Command[0] != "/bin/sh" || s.Command[1] != "-c" {
		t.Fatalf("command = %v, want /bin/sh -c <script>", s.Command)
	}
	for _, want := range []string{"manage.py migrate", "createsuperuser", "gunicorn"} {
		if !strings.Contains(s.Command[2], want) {
			t.Errorf("the command script does not mention %q:\n%s", want, s.Command[2])
		}
	}
	if got, want := s.Environment["DB_HOST"], "hmd_db"; got != want {
		t.Errorf("DB_HOST = %q, want %q -- the alias the CLI attaches to the RDS container", got, want)
	}
}

// A service with no container_name gets compose's own <project>-<service>-1.
func TestServiceNameFallsBackToTheComposeDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		svc  Service
		want string
	}{
		{"a pinned name wins", Service{Key: "proxy", ContainerName: "hmd_proxy"}, "hmd_proxy"},
		{"no pinned name", Service{Key: "worker"}, "proj-worker-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.svc.Name("proj"); got != tt.want {
				t.Errorf("Name() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParsePort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    string
		want    Port
		wantErr bool
	}{
		{"a simple pair", "80:80", Port{80, 80, 80, 80, "tcp"}, false},
		{"differing ports", "8080:80", Port{8080, 8080, 80, 80, "tcp"}, false},
		{"a range", "19000-19079:19000-19079", Port{19000, 19079, 19000, 19079, "tcp"}, false},
		{"an explicit protocol", "53:53/udp", Port{53, 53, 53, 53, "udp"}, false},
		{"container only", "80", Port{0, 0, 80, 80, "tcp"}, false},
		{"mismatched range lengths", "1-2:1-3", Port{}, true},
		{"a backwards range", "19079-19000:19079-19000", Port{}, true},
		{"not a number", "abc:80", Port{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parsePort(tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parsePort(%q) = %+v, want an error", tt.spec, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePort(%q): %v", tt.spec, err)
			}
			if got != tt.want {
				t.Errorf("parsePort(%q) = %+v, want %+v", tt.spec, got, tt.want)
			}
		})
	}
}

func TestParseVolumeRejectsANamedVolume(t *testing.T) {
	t.Parallel()

	// Treating a named volume as a host path would silently create a directory
	// instead of failing.
	if _, err := parseVolumes([]string{"somevolume:/data"}, fakeEnv(nil)); err == nil {
		t.Error("a named volume was accepted, want a refusal")
	}
	if _, err := parseVolumes([]string{"/host:/data:ro"}, fakeEnv(nil)); err != nil {
		t.Errorf("a bind mount was rejected: %v", err)
	}
	if _, err := parseVolumes([]string{"/host:/data:nonsense"}, fakeEnv(nil)); err == nil {
		t.Error("an unsupported mode was accepted, want a refusal")
	}
}

func TestParseFailsOnAnUnexpandableValue(t *testing.T) {
	t.Parallel()

	// A value that cannot expand should fail while there is still a file to
	// name, not at container-create time.
	_, err := Parse([]byte("services:\n  a:\n    image: ${VAR:?required}\n"), "p", fakeEnv(nil))
	if err == nil {
		t.Error("Parse accepted an unsupported substitution")
	}
}

func portKey(p Port) string {
	return fmt.Sprintf("%d-%d:%d-%d", p.HostStart, p.HostEnd, p.ContainerStart, p.ContainerEnd)
}

// The identity provider is the nsctl image: the Dockerfile's ENTRYPOINT is
// /nsctl and its CMD is only a default. The image is never published, so the
// compose file must name the tag nsctl sets (HMD_NSCTL_IMAGE) or enabling auth
// would try to pull an image nothing builds.
func TestAuthdIsTheNsctlImage(t *testing.T) {
	t.Parallel()

	p := parseControlPlane(t, controlPlaneEnv())
	authd := service(t, p, "authd")

	if !strings.HasPrefix(authd.Image, "hmd-img-nsctl:") {
		t.Errorf("authd runs %q; want the local hmd-img-nsctl image", authd.Image)
	}
	if got := strings.Join(authd.Command, " "); got != "authd serve --addr :8080" {
		t.Errorf("authd command = %q", got)
	}
	if len(authd.Profiles) != 1 || authd.Profiles[0] != "authd" {
		t.Errorf("profiles = %v, want the authd profile: it is off by default", authd.Profiles)
	}
	for _, port := range authd.Ports {
		t.Errorf("authd publishes %v; only hmd_proxy may publish, which is what lets one issuer URL "+
			"work from the host, a pod and a Floci container alike", port)
	}
}

// The proxy answers for the issuer's hostname on the platform network, so a
// Floci Lambda resolves the same name the browser and the cluster do. Three
// resolvers, one string -- a consumer that fetched keys under one name and
// reads `iss` as another rejects every token.
func TestTheProxyAnswersForTheIssuerHostname(t *testing.T) {
	t.Parallel()

	s := service(t, parseControlPlane(t, controlPlaneEnv()), "proxy")
	if len(s.Networks) != 1 {
		t.Fatalf("proxy networks = %v", s.Networks)
	}
	var found bool
	for _, alias := range s.Networks[0].Aliases {
		if alias == "auth.local.neuronsphere.io" {
			found = true
		}
	}
	if !found {
		t.Errorf("aliases = %v, want the identity provider's issuer hostname", s.Networks[0].Aliases)
	}
}
