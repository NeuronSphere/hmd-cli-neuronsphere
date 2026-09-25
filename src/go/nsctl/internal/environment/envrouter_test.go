package environment

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
)

// The whole point of NERD027: the ports bound are a function of what is running,
// not of what could theoretically run. Sixteen environments times four reserved
// slots bound 47 ports at every start that nothing could ever answer on.
func TestAnEnvironmentPublishesOnlyThePortsItUses(t *testing.T) {
	t.Parallel()

	local := &registry.Environment{Slug: "local", Name: "local", PortSlot: 0, PortBase: 19000}
	dev := &registry.Environment{Slug: "dev2", Name: "dev2", PortSlot: 1, PortBase: 19000}

	tests := []struct {
		name    string
		env     *registry.Environment
		cluster bool
		trino   bool
		want    []int
	}{
		{
			// Nothing running: nothing published. The old scheme bound four
			// slot ports here regardless.
			name: "no cluster", env: dev, want: nil,
		},
		{
			// A cluster and no Trino is the ordinary case for an environment
			// that was never given one.
			name: "cluster only", env: dev, cluster: true,
			want: []int{19065},
		},
		{
			name: "cluster and trino", env: dev, cluster: true, trino: true,
			want: []int{19005, 19065},
		},
		{
			// The default environment additionally keeps the historical 18080,
			// so existing integration tests need no change -- but only once a
			// coordinator exists to answer on it.
			name: "default env with trino", env: local, cluster: true, trino: true,
			want: []int{18080, 19001, 19064},
		},
		{
			name: "default env without trino", env: local, cluster: true,
			want: []int{19064},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := desiredRouterPorts(tt.env, tt.cluster, tt.trino)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("desiredRouterPorts = %v, want %v", got, tt.want)
			}
		})
	}
}

// fakeRouterDocker records what the ensure step did.
type fakeRouterDocker struct {
	exists    bool
	running   bool
	published []int
	removed   []string
	runArgs   []string
	runErr    error
}

func (f *fakeRouterDocker) Exists(_ context.Context, _ string) bool { return f.exists }
func (f *fakeRouterDocker) Running(_ context.Context, _ string) (bool, error) {
	return f.running, nil
}
func (f *fakeRouterDocker) PublishedPorts(_ context.Context, _ string) []int { return f.published }
func (f *fakeRouterDocker) RemoveContainer(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	return nil
}
func (f *fakeRouterDocker) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	f.runArgs = args
	return nil, nil, f.runErr
}

// Recreated only when the set changes. An ordinary restart with the same ports
// must not churn a kubectl session for nothing.
func TestTheRouterIsRecreatedOnlyWhenItsPortsChange(t *testing.T) {
	t.Parallel()

	env := &registry.Environment{Slug: "dev2", Name: "dev2", PortSlot: 1, PortBase: 19000,
		RouterContainer: "hmd_router-dev2"}

	same := &fakeRouterDocker{exists: true, running: true, published: []int{19005, 19065}}
	if err := ensureEnvRouter(context.Background(), same, "/home", "net", "proj", env, true, true); err != nil {
		t.Fatalf("ensureEnvRouter: %v", err)
	}
	if len(same.removed) != 0 || same.runArgs != nil {
		t.Errorf("an unchanged port set recreated the container: removed=%v ran=%v", same.removed, same.runArgs)
	}

	// Trino appears, because something deployed it: the set changed, so the
	// container is replaced.
	changed := &fakeRouterDocker{exists: true, running: true, published: []int{19065}}
	if err := ensureEnvRouter(context.Background(), changed, "/home", "net", "proj", env, true, true); err != nil {
		t.Fatalf("ensureEnvRouter: %v", err)
	}
	if len(changed.removed) != 1 {
		t.Errorf("a changed port set did not remove the old container: %v", changed.removed)
	}
	if changed.runArgs == nil {
		t.Fatal("a changed port set did not create a new container")
	}
	args := joinArgs(changed.runArgs)
	for _, want := range []string{
		"--name hmd_router-dev2",
		"-p 19005:19005",
		"-p 19065:19065",
		// Labelled with the environment's compose project, which is what lets
		// OurPorts count these as ours rather than as a foreign listener.
		"--label com.docker.compose.project=proj",
		"--network net",
	} {
		if !contains(args, want) {
			t.Errorf("the run command is missing %q:\n%s", want, args)
		}
	}
}

// An environment with nothing to publish has no router at all, and one left over
// from a set that is now empty is removed -- a router holding ports the next
// start routes around is a confusing way to lose a port.
func TestARouterWithNothingToPublishIsRemoved(t *testing.T) {
	t.Parallel()

	env := &registry.Environment{Slug: "dev2", Name: "dev2", PortSlot: 1, PortBase: 19000,
		RouterContainer: "hmd_router-dev2"}

	d := &fakeRouterDocker{exists: true, running: true, published: []int{19065}}
	if err := ensureEnvRouter(context.Background(), d, "/home", "net", "proj", env, false, false); err != nil {
		t.Fatalf("ensureEnvRouter: %v", err)
	}
	if len(d.removed) != 1 {
		t.Errorf("a router with nothing to publish was left behind: %v", d.removed)
	}
	if d.runArgs != nil {
		t.Errorf("it was recreated with no ports: %v", d.runArgs)
	}

	// And one that never existed is not created.
	absent := &fakeRouterDocker{}
	if err := ensureEnvRouter(context.Background(), absent, "/home", "net", "proj", env, false, false); err != nil {
		t.Fatalf("ensureEnvRouter: %v", err)
	}
	if absent.runArgs != nil || len(absent.removed) != 0 {
		t.Errorf("an environment publishing nothing touched the engine: %v %v", absent.runArgs, absent.removed)
	}
}

// A failure to create it is reported, not swallowed: the kubeconfig points at a
// port this container is what publishes.
func TestARouterThatCannotBeCreatedIsAnError(t *testing.T) {
	t.Parallel()

	env := &registry.Environment{Slug: "dev2", Name: "dev2", PortSlot: 1, PortBase: 19000,
		RouterContainer: "hmd_router-dev2"}
	d := &fakeRouterDocker{runErr: errors.New("port is already allocated")}
	if err := ensureEnvRouter(context.Background(), d, "/home", "net", "proj", env, true, false); err == nil {
		t.Error("a failed create should be reported")
	}
}

func joinArgs(args []string) string {
	out := ""
	for _, a := range args {
		out += a + " "
	}
	return out
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// nameFlag is the value the run command gave --name.
func nameFlag(args []string) string {
	for i, a := range args {
		if a == "--name" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// The name the reload exec's into is the name the engine was told to create.
//
// It was derived in two places and they drifted: the home-scoping NERD027's
// acceptance run added reached registry.RouterContainerName and not
// internal/router, so every `nsctl env start` exec'd `nginx -t` into
// `hmd_router-<slug>`, which nothing creates, and reported the daemon's "No
// such container" under the headline "the nginx configuration is invalid".
// Nothing was ever reloaded.
func TestTheRouterIsReloadedUnderTheNameItWasCreatedWith(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	// Built the way a loaded registry builds it -- the field is json:"-" and is
	// backfilled from the home and the slug on every read.
	env := &registry.Environment{Slug: "dev2", Name: "dev2", PortSlot: 1, PortBase: 19000,
		RouterContainer: registry.RouterContainerName(home, "dev2")}

	d := &fakeRouterDocker{}
	if err := ensureEnvRouter(context.Background(), d, home, "net", "proj", env, true, false); err != nil {
		t.Fatalf("ensureEnvRouter: %v", err)
	}
	created := nameFlag(d.runArgs)
	if created == "" {
		t.Fatalf("the router was not created: %v", d.runArgs)
	}
	if created == "hmd_router-"+env.Slug {
		t.Errorf("the router container is not scoped to its home: %q", created)
	}

	var execed []string
	exec := func(_ context.Context, container string, _ ...string) ([]byte, error) {
		execed = append(execed, container)
		return nil, nil
	}
	if err := router.NewEnv(home, env.Slug, nil).Reload(context.Background(), exec); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(execed) == 0 {
		t.Fatal("the reload exec'd nothing")
	}
	for _, got := range execed {
		if got != created {
			t.Errorf("the reload exec'd into %q, but the container created is %q", got, created)
		}
	}
}
