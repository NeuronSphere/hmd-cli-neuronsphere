package cmd

import "testing"

// The opt-in is recorded in the environment manifest, survives a later
// `repo add`, and is cleared as absence like the substrate's default.
func TestRecordHostWorkspaceRoundTrips(t *testing.T) {
	t.Parallel()

	home, env := repoEnv(t, "hmd-ms-myapi")
	opts := &Options{Version: "test"}
	opts.resolve(home, fakeEnv(env), func(string) {})

	if err := recordHostWorkspace(opts, home, "local", true); err != nil {
		t.Fatal(err)
	}
	if m := readManifest(t, env, "local"); !m.HostWorkspace {
		t.Errorf("recorded %+v, want host_workspace", m)
	}

	if _, _, err := run(t, fakeEnv(env), "repo", "add", "hmd-ms-myapi"); err != nil {
		t.Fatal(err)
	}
	if m := readManifest(t, env, "local"); !m.HostWorkspace {
		t.Errorf("repo add dropped host_workspace: %+v", m)
	}

	if err := recordHostWorkspace(opts, home, "local", false); err != nil {
		t.Fatal(err)
	}
	if m := readManifest(t, env, "local"); m.HostWorkspace || len(m.Repos) != 1 {
		t.Errorf("recorded %+v, want host_workspace cleared and the instance kept", m)
	}
}
