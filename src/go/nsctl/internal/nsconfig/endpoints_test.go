package nsconfig

import (
	"strings"
	"testing"
)

func lookupFrom(vars map[string]string) Lookup {
	return func(k string) string { return vars[k] }
}

// TestResolveEndpointPrecedence walks the five tiers in order, each case
// supplying everything the tiers below it would have used -- so a tier that
// stopped winning would be caught by the case above it rather than by the
// absence of a case.
func TestResolveEndpointPrecedence(t *testing.T) {
	t.Parallel()

	full := Profile{
		Name:                 "acme",
		CustomerCode:         "acme",
		Region:               "reg1",
		DeploymentURL:        "https://deploy.profile.example",
		ArtifactLibrarianURL: "https://lib.profile.example",
	}
	env := map[string]string{
		ArtifactLibrarianURLEnv: "https://lib.env.example",
		DeploymentURLEnv:        "https://deploy.env.example",
		CustomerCodeEnv:         "envco",
		RegionEnv:               "reg9",
	}

	tests := []struct {
		name    string
		service Service
		flag    string
		profile Profile
		vars    map[string]string
		want    string
		source  string
	}{
		{
			name: "the flag beats everything", service: ArtifactLibrarian,
			flag: "https://flag.example/", profile: full, vars: env,
			want: "https://flag.example", source: "--url",
		},
		{
			name: "the environment beats the profile", service: ArtifactLibrarian,
			profile: full, vars: env,
			want: "https://lib.env.example", source: "$" + ArtifactLibrarianURLEnv,
		},
		{
			name: "the profile's URL beats composition", service: ArtifactLibrarian,
			profile: full, vars: map[string]string{CustomerCodeEnv: "envco", RegionEnv: "reg9"},
			want: "https://lib.profile.example", source: "artifact_librarian_url in profile \"acme\"",
		},
		{
			name: "the profile's tenant beats the environment's", service: ArtifactLibrarian,
			profile: Profile{Name: "acme", CustomerCode: "acme", Region: "reg1"},
			vars:    map[string]string{CustomerCodeEnv: "envco", RegionEnv: "reg9"},
			want:    "https://artifact-aaa-reg1.acme-admin-neuronsphere.io",
			source:  "customer_code and region in profile \"acme\"",
		},
		{
			name: "the environment's tenant is the last resort", service: ArtifactLibrarian,
			vars:   map[string]string{CustomerCodeEnv: "envco", RegionEnv: "reg9"},
			want:   "https://artifact-aaa-reg9.envco-admin-neuronsphere.io",
			source: "$" + CustomerCodeEnv + " and $" + RegionEnv,
		},
		{
			name: "the deployment service resolves by the same rule", service: Deployment,
			profile: full, vars: map[string]string{},
			want: "https://deploy.profile.example", source: "deployment_url in profile \"acme\"",
		},
		{
			name: "the deployment hostname is composed too", service: Deployment,
			profile: Profile{Name: "acme", CustomerCode: "acme", Region: "reg1"},
			want:    "https://ms-deployment-aaa-reg1.acme-admin-neuronsphere.io",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveEndpoint(tt.service, tt.flag, tt.profile, lookupFrom(tt.vars))
			if err != nil {
				t.Fatal(err)
			}
			if got.URL != tt.want {
				t.Errorf("URL = %q, want %q", got.URL, tt.want)
			}
			if tt.source != "" && got.Source != tt.source {
				t.Errorf("Source = %q, want %q", got.Source, tt.source)
			}
		})
	}
}

// TestComposedHostnamesArePinned fixes both templates against the Python that
// composes them. A wrong host does not fail as "wrong host" -- it fails as a
// DNS error that reads like a network problem.
func TestComposedHostnamesArePinned(t *testing.T) {
	t.Parallel()

	profile := Profile{CustomerCode: "hmdtr1", Region: "reg1"}
	for _, tt := range []struct {
		service Service
		want    string
	}{
		// hmd_lib_librarian_client/artifact_tools.py
		{ArtifactLibrarian, "https://artifact-aaa-reg1.hmdtr1-admin-neuronsphere.io"},
		// hmd-cli-ns-bootstrap and hmd-cli-deploy, both with "admin" hardcoded
		{Deployment, "https://ms-deployment-aaa-reg1.hmdtr1-admin-neuronsphere.io"},
	} {
		got, err := ResolveEndpoint(tt.service, "", profile, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.URL != tt.want {
			t.Errorf("%s composed %q, want %q", tt.service.Label, got.URL, tt.want)
		}
	}
}

// TestShadowedRecordsWhatTheEnvironmentBeat is the guard SPEC001 asks for: the
// override still wins, and the fact that it overrode a profile someone wrote
// down is not lost.
func TestShadowedRecordsWhatTheEnvironmentBeat(t *testing.T) {
	t.Parallel()

	profile := Profile{Name: "acme", ArtifactLibrarianURL: "https://lib.profile.example"}

	got, err := ResolveEndpoint(ArtifactLibrarian, "",
		profile, lookupFrom(map[string]string{ArtifactLibrarianURLEnv: "https://lib.env.example"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://lib.env.example" {
		t.Errorf("URL = %q, want the environment's", got.URL)
	}
	if got.Shadowed != "https://lib.profile.example" {
		t.Errorf("Shadowed = %q, want the profile's URL", got.Shadowed)
	}

	// The same value from both is not a shadowing, and warning about it would
	// train people to ignore the warning.
	same, err := ResolveEndpoint(ArtifactLibrarian, "",
		profile, lookupFrom(map[string]string{ArtifactLibrarianURLEnv: "https://lib.profile.example/"}))
	if err != nil {
		t.Fatal(err)
	}
	if same.Shadowed != "" {
		t.Errorf("Shadowed = %q, want none when both name the same address", same.Shadowed)
	}
}

// TestUnresolvedNamesEveryTier is acceptance criterion 7. A refusal that named
// only the tier the reader happened to miss would send them to the docs.
func TestUnresolvedNamesEveryTier(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		service Service
		profile Profile
		vars    map[string]string
	}{
		{ArtifactLibrarian, Profile{Name: "acme"}, nil},
		{Deployment, Profile{Name: "acme"}, nil},
		// Half a tenant is not a tenant.
		{Deployment, Profile{Name: "acme", CustomerCode: "acme"}, nil},
		{Deployment, Profile{Name: "acme", Region: "reg1"}, nil},
		{Deployment, Profile{}, map[string]string{CustomerCodeEnv: "acme"}},
	} {
		_, err := ResolveEndpoint(tt.service, "", tt.profile, lookupFrom(tt.vars))
		if err == nil {
			t.Fatalf("%s resolved with nothing configured", tt.service.Label)
		}
		msg := err.Error()
		for _, want := range []string{
			"--url", tt.service.URLEnv, tt.service.ProfileKey,
			"customer_code and region", CustomerCodeEnv, RegionEnv, "never guesses",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s refusal does not mention %q:\n%s", tt.service.Label, want, msg)
			}
		}
	}
}

// TestProfileEndpointKeysAreOptional is acceptance criterion 8: a file written
// before this document still parses, and nsctl login still reads it.
func TestProfileEndpointKeysAreOptional(t *testing.T) {
	t.Parallel()

	cfg, err := Parse([]byte("[profile.acme]\nauth_url = \"https://auth.example/oauth2/ns\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := cfg.Profile("acme")
	if err != nil {
		t.Fatal(err)
	}
	if p.DeploymentURL != "" || p.ArtifactLibrarianURL != "" || p.CustomerCode != "" || p.Region != "" {
		t.Errorf("unset keys came back set: %+v", p)
	}
}

// TestProfileRefusesAServiceURLThatIsNotOne catches the misconfiguration at the
// point it can be fixed, rather than as a connection error naming a host that
// appears nowhere in the file.
func TestProfileRefusesAServiceURLThatIsNotOne(t *testing.T) {
	t.Parallel()

	cfg, err := Parse([]byte("[profile.acme]\nauth_url = \"https://auth.example/oauth2/ns\"\n" +
		"deployment_url = \"ms-deployment.example\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.Profile("acme")
	if err == nil || !strings.Contains(err.Error(), "deployment_url") {
		t.Fatalf("Profile() error = %v, want one naming deployment_url", err)
	}
}
