package bom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

func names(entries []Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.RepoInstanceName)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// NERD014 SPEC004: none is empty, core is the three that need no cluster, full
// is what Substrate already yields.
func TestSubstrateForPerMode(t *testing.T) {
	t.Parallel()

	env := testEnv()
	cases := []struct {
		mode        manifest.Substrate
		withCluster bool
		want        []string
	}{
		{manifest.SubstrateNone, true, nil},
		{manifest.SubstrateNone, false, nil},
		{manifest.SubstrateCore, true, []string{CoreInstanceName, BaseVPCInstance, EnvDBInstance}},
		{manifest.SubstrateCore, false, []string{CoreInstanceName, BaseVPCInstance, EnvDBInstance}},
		{manifest.SubstrateFull, true, names(Substrate(env, true))},
		{manifest.SubstrateFull, false, names(Substrate(env, false))},
	}
	for _, c := range cases {
		got := names(SubstrateFor(env, c.mode, c.withCluster))
		if !equal(got, c.want) {
			t.Errorf("SubstrateFor(%s, cluster=%v) = %v, want %v", c.mode, c.withCluster, got, c.want)
		}
	}
}

// The environment variable is ANDed with the mode, never replaced by it: full
// with k3s disabled is a full whose cluster is elsewhere, as today.
func TestSubstrateForFullHonoursTheClusterGate(t *testing.T) {
	t.Parallel()

	with := names(SubstrateFor(testEnv(), manifest.SubstrateFull, true))
	without := names(SubstrateFor(testEnv(), manifest.SubstrateFull, false))
	if len(with) != len(without)+1 || with[len(with)-1] != EKSClusterInstance {
		t.Errorf("with cluster %v, without %v", with, without)
	}
}

// What repo list prints as substrate rows: only what the mode deploys.
func TestSubstrateNamesPerMode(t *testing.T) {
	t.Parallel()

	if got := SubstrateNames(manifest.SubstrateNone); len(got) != 0 {
		t.Errorf("none names %v", got)
	}
	if got := SubstrateNames(manifest.SubstrateCore); !equal(got, []string{CoreInstanceName, BaseVPCInstance, EnvDBInstance}) {
		t.Errorf("core names %v", got)
	}
	if got := SubstrateNames(manifest.SubstrateFull); !equal(got, []string{CoreInstanceName, BaseVPCInstance, EnvDBInstance, EKSClusterInstance}) {
		t.Errorf("full names %v", got)
	}
}

// A name stays reserved whether or not this environment deploys it.
func TestIsSubstrateIgnoresTheMode(t *testing.T) {
	t.Parallel()

	for _, n := range SubstrateNames(manifest.SubstrateFull) {
		if !IsSubstrate(n) {
			t.Errorf("%s is not substrate", n)
		}
	}
}

// A changeset with no core instance, against a graph that has never registered
// one, declares nothing for the core instead of failing on the missing
// RepoClassVersion -- that is every Phase B under substrate none.
func TestSeedSkipsTheCoreDeclarationWhenNoCoreExists(t *testing.T) {
	t.Parallel()

	var ops []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := strings.TrimPrefix(r.URL.Path, "/apiop/")
		ops = append(ops, op)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(op, "find_repo_class_versions/") {
			// The core was never registered; the user's class was.
			if strings.HasSuffix(op, CoreRepoClass) {
				_, _ = w.Write([]byte(`[]`))
			} else {
				_, _ = w.Write([]byte(`[{"identifier":"rcv-1","version":"0.1"}]`))
			}
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	s := &Seeder{
		Client:   msdeploy.New(srv.URL),
		Versions: declaringResolver{map[string][]repoclass.ResourceDeclaration{"acme-dp": {auroraPostgres}}},
	}
	_, _ = s.Seed(context.Background(), Environment{Slug: "local", AccountID: "1", Region: "reg1"},
		[]Entry{{RepoInstanceName: "acme-dp", RepoClassName: "acme-dp", RepoClassVersion: "0.1"}})

	// The core's lookup found nothing, and no declaration follows it: the next
	// call is the user's own lookup. Nothing errors out before the user's
	// class declares what it produces.
	coreLookup := -1
	for i, op := range ops {
		if op == "find_repo_class_versions/"+CoreRepoClass {
			coreLookup = i
		}
	}
	if coreLookup < 0 {
		t.Fatalf("the core was never looked up; calls were %v", ops)
	}
	if next := ops[coreLookup+1]; next != "find_repo_class_versions/acme-dp" {
		t.Errorf("after the core lookup came %q, want the user's lookup with nothing declared for the core (calls: %v)", next, ops)
	}
	if !slices.Contains(ops, "declare_produces_resource_definition") {
		t.Errorf("the user's class declared nothing; calls were %v", ops)
	}
}
