package container

import "testing"

func TestHMDHomeHashMatchesThePythonValue(t *testing.T) {
	t.Parallel()

	// The cross-language anchor. env_registry._hmd_home_hash computes
	// sha256(os.path.abspath(HMD_HOME))[:8]; this value is the one a live
	// install produced, visible in its network name
	// (neuronsphere_default-57aa833c) and compose project
	// (local_neuronsphere-57aa833c).
	const home = "/Users/aburg/hmdtr1"
	const want = "57aa833c"

	if got := HMDHomeHash(home); got != want {
		t.Errorf("HMDHomeHash(%q) = %q, want %q -- nsctl would name its containers differently from every other HMD tool", home, got, want)
	}
}

func TestHMDHomeHashIsEmptyForAnEmptyHome(t *testing.T) {
	t.Parallel()

	if got := HMDHomeHash(""); got != "" {
		t.Errorf("HMDHomeHash(\"\") = %q, want empty", got)
	}
}

func TestHMDHomeHashDoesNotResolveSymlinks(t *testing.T) {
	t.Parallel()

	// A trailing slash and a redundant segment must normalise the same way
	// Python's abspath does; a symlink must NOT be resolved.
	a := HMDHomeHash("/Users/aburg/hmdtr1")
	b := HMDHomeHash("/Users/aburg/hmdtr1/")
	c := HMDHomeHash("/Users/aburg/foo/../hmdtr1")
	if a != b || a != c {
		t.Errorf("path normalisation differs: %q %q %q", a, b, c)
	}
}

func TestDefaultNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		home        string
		wantNetwork string
		wantProject string
	}{
		{"a real home is suffixed", "/Users/aburg/hmdtr1", "neuronsphere_default-57aa833c", "local_neuronsphere-57aa833c"},
		{"no home falls back to the bare names", "", "neuronsphere_default", "local_neuronsphere"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := DefaultNetwork(tt.home); got != tt.wantNetwork {
				t.Errorf("DefaultNetwork() = %q, want %q", got, tt.wantNetwork)
			}
			if got := DefaultProject(tt.home); got != tt.wantProject {
				t.Errorf("DefaultProject() = %q, want %q", got, tt.wantProject)
			}
		})
	}
}
