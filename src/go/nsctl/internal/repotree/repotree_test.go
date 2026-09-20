package repotree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bundled"
)

// The whole point of the item: a machine with no HMD_REPO_HOME must be able to
// deploy the substrate. Before this, the first node failed with "no working
// tree for hmd-vpc" and a Homebrew install stopped after control-plane start.
func TestTheSubstrateAndFoundationTreesAreCarried(t *testing.T) {
	t.Parallel()

	// The control plane's own instances, the environment substrate, the
	// foundation services, and the two operators every cloud chart's
	// ExternalSecrets need to resolve.
	for _, class := range []string{
		"hmd-vpc", "hmd-postgres-rds", "hmd-inf-neptune", "hmd-inf-eks-cluster",
		// The deployment service ships as its registry-and-resolver core
		// (NERD0015); the orchestrator's tree is never needed locally.
		"hmd-ms-naming", "hmd-ms-artifact-lib", "hmd-ms-deployment-core", "hmd-ms-dbaccount",
		"hmd-inf-ext-secrets-crds", "hmd-inf-ext-secrets",
	} {
		if !Available(class) {
			t.Errorf("no bundled tree for %s; a deploy of it needs $HMD_REPO_HOME", class)
		}
	}
	// And not the workloads: superset, hyperdx and the rest are plugin content
	// and would be dead weight in every binary.
	for _, class := range []string{"hmd-inf-superset", "hmd-inf-hyperdx", "hmd-inf-s3bucket", "hmd-ms-deployment"} {
		if Available(class) {
			t.Errorf("%s is bundled; workloads are RepoClasses a user adds, not things nsctl ships", class)
		}
	}
}

// A tree has to be on disk at a host path, because projectbuilder is a sibling
// container and the daemon resolves its bind mounts on the host. One that
// exists only inside the binary is not mountable.
func TestATreeIsMaterialisedOnDiskWithItsMetadata(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := Dir(home, "hmd-vpc")
	if dir == "" {
		t.Fatal("hmd-vpc did not materialise")
	}
	if !strings.HasPrefix(dir, Root(home)) {
		t.Errorf("materialised outside HMD_HOME at %s; the runner container mounts HMD_HOME and nothing else", dir)
	}
	// ResolveVersion and floci.ServiceConfig read exactly these.
	for _, rel := range []string{"meta-data/VERSION", "meta-data/manifest.json"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s is missing from the materialised tree: %v", rel, err)
		}
	}
}

// The directory carries the archive's digest, so an unpack happens once per
// version rather than once per deploy -- and two nsctl versions on one HMD_HOME
// do not overwrite each other's trees.
func TestMaterialisingTwiceIsTheSameDirectory(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	first := Dir(home, "hmd-vpc")
	second := Dir(home, "hmd-vpc")
	if first != second {
		t.Errorf("two calls gave %q and %q", first, second)
	}
	data, _ := bundled.RepoArchive("hmd-vpc")
	if len(data) == 0 {
		t.Fatal("no archive for hmd-vpc")
	}
	if base := filepath.Base(first); !strings.Contains(base, "@") {
		t.Errorf("the directory %q is not digest-addressed, so a version bump would reuse a stale tree", base)
	}
}

// A class the binary does not carry is nothing, not an empty directory a caller
// would then mount.
func TestAnUncarriedClassMaterialisesNothing(t *testing.T) {
	t.Parallel()

	if dir := Dir(t.TempDir(), "hmd-inf-not-bundled"); dir != "" {
		t.Errorf("Dir = %q for a class carrying no tree", dir)
	}
}

// An archive entry naming a path outside the destination is refused rather than
// written. These archives are built here, but the unpacker is the thing that
// has to be sure of it.
func TestUnpackRefusesToEscape(t *testing.T) {
	t.Parallel()

	if _, err := safeJoin("/tmp/dest", "../../etc/passwd"); err == nil {
		t.Error("safeJoin allowed an entry outside the destination")
	}
}
