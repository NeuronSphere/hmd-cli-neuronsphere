package nsrunner

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/runner"
)

// The runner resolves a deploy's context from HMD_HOME, and lets a submitter
// pin any of it. Both halves are easy to extend and easy to extend wrongly:
// adding a field to ConfigOverride without wiring it into applyOverride
// compiles, submits, and silently ignores what the caller asked for.

func TestEveryOverrideFieldIsHonoured(t *testing.T) {
	// Reflective on purpose. A test listing the fields by hand would pass
	// forever after someone adds the fifteenth, which is the whole failure
	// mode -- the CLI pins a value, the runner drops it, and the deploy runs
	// against the wrong network or account with nothing to say so.
	overrideType := reflect.TypeOf(ConfigOverride{})
	cfgType := reflect.TypeOf(runner.Config{})
	_ = cfgType

	var o ConfigOverride
	ov := reflect.ValueOf(&o).Elem()
	for i := 0; i < overrideType.NumField(); i++ {
		f := ov.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("pinned-" + overrideType.Field(i).Name)
		case reflect.Map:
			f.Set(reflect.ValueOf(map[string]string{"k": "pinned"}))
		default:
			t.Fatalf("ConfigOverride.%s is a %s, which this test does not know how to pin",
				overrideType.Field(i).Name, f.Kind())
		}
	}

	cfg := runner.Config{}
	applyOverride(&cfg, &o)

	cv := reflect.ValueOf(cfg)
	for i := 0; i < overrideType.NumField(); i++ {
		name := overrideType.Field(i).Name
		target := cv.FieldByName(name)
		if !target.IsValid() {
			t.Fatalf("ConfigOverride.%s has no counterpart on runner.Config", name)
		}
		switch target.Kind() {
		case reflect.String:
			if target.String() != "pinned-"+name {
				t.Errorf("%s: applyOverride did not carry the pinned value (got %q)",
					name, target.String())
			}
		case reflect.Map:
			if target.Len() == 0 {
				t.Errorf("%s: applyOverride dropped the pinned map", name)
			}
		}
	}
}

func TestAnEmptyOverrideLeavesTheResolvedValue(t *testing.T) {
	// set() is what makes "pinned nothing" different from "pinned empty".
	cfg := runner.Config{Network: "resolved", AccountID: "000000000001"}
	applyOverride(&cfg, &ConfigOverride{Network: "", AccountID: "000000000002"})

	if cfg.Network != "resolved" {
		t.Errorf("an unset override overwrote the resolved value: %q", cfg.Network)
	}
	if cfg.AccountID != "000000000002" {
		t.Errorf("a set override was not applied: %q", cfg.AccountID)
	}
}

func TestWorkDirIsNotOverridable(t *testing.T) {
	// Deliberate, and worth pinning: WorkDir names a path that must exist
	// identically on the host and inside this container, and only the runner
	// knows what it was given. A submitter pinning it would compose bind-mount
	// sources the host daemon cannot resolve.
	cfg := runner.Config{WorkDir: "/hmd/.cache/nsrunner/work"}
	applyOverride(&cfg, &ConfigOverride{})
	if cfg.WorkDir != "/hmd/.cache/nsrunner/work" {
		t.Errorf("WorkDir changed: %q", cfg.WorkDir)
	}
	if _, ok := reflect.TypeOf(ConfigOverride{}).FieldByName("WorkDir"); ok {
		t.Error("ConfigOverride grew a WorkDir field; see the comment in applyOverride")
	}
}

func TestProjectBuilderRefDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "nothing set",
			env:  map[string]string{},
			want: "ghcr.io/neuronsphere/hmd-img-projectbuilder:stable",
		},
		{
			name: "a different registry",
			env:  map[string]string{"HMD_LOCAL_NS_CONTAINER_REGISTRY": "ghcr.io/hmdlabs"},
			want: "ghcr.io/hmdlabs/hmd-img-projectbuilder:stable",
		},
		{
			name: "a pinned version",
			env:  map[string]string{"HMD_PROJECTBUILDER_VERSION": "0.9"},
			want: "ghcr.io/neuronsphere/hmd-img-projectbuilder:0.9",
		},
		{
			name: "both",
			env: map[string]string{
				"HMD_LOCAL_NS_CONTAINER_REGISTRY": "ghcr.io/hmdlabs",
				"HMD_PROJECTBUILDER_VERSION":      "0.9",
			},
			want: "ghcr.io/hmdlabs/hmd-img-projectbuilder:0.9",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(k string) string { return tc.env[k] }
			if got := projectBuilderRef(lookup); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTheRunnersDirectoriesLiveUnderHome(t *testing.T) {
	// Not cosmetic. Both become bind-mount sources resolved by the host
	// daemon, so they have to exist at the same absolute path inside the
	// runner container and outside it -- which is what HMD_HOME already is and
	// the system temp dir is not.
	const home = "/Users/someone/hmdtr1"
	for _, got := range []string{WorkflowDir(home), DefaultWorkDir(home)} {
		if !filepath.IsAbs(got) {
			t.Errorf("%q is not absolute", got)
		}
		if rel, err := filepath.Rel(home, got); err != nil || rel == "" || strings.HasPrefix(rel, "..") {
			t.Errorf("%q is not under HMD_HOME", got)
		}
	}
	if WorkflowDir(home) == DefaultWorkDir(home) {
		t.Error("records and workspaces share a directory")
	}
}
