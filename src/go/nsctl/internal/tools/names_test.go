package tools

import "testing"

// The expected values in this table were produced by calling the real
// hmd_cli_tools.make_standard_name, not by reading it. SPEC014 rates a
// divergence here MEDIUM precisely because it fails inside a Python
// microservice, far from the Go that caused it -- so the fixture has to come
// from the function it must agree with.
func TestMakeStandardName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                                                               string
		instance, repo, deploymentID, environment, hmdRegion, customerCode string
		want                                                               string
	}{
		{
			name:     "the environment database",
			instance: "environment-db", repo: "hmd-postgres-rds",
			deploymentID: "local", environment: "local", hmdRegion: "reg1", customerCode: "hmdtr1",
			want: "environment-db_hmd-postgres-rds_local_local_reg1_hmdtr1",
		},
		{
			name:     "the graph cluster",
			instance: "global-graph", repo: "hmd-inf-neptune",
			deploymentID: "local", environment: "local", hmdRegion: "reg1", customerCode: "hmdtr1",
			want: "global-graph_hmd-inf-neptune_local_local_reg1_hmdtr1",
		},
		{
			name:     "a service lambda",
			instance: "transform", repo: "hmd-ms-transform",
			deploymentID: "local", environment: "local", hmdRegion: "reg1", customerCode: "hmdtr1",
			want: "transform_hmd-ms-transform_local_local_reg1_hmdtr1",
		},
		{
			name:     "single characters",
			instance: "a", repo: "b", deploymentID: "c", environment: "d", hmdRegion: "e", customerCode: "f",
			want: "a_b_c_d_e_f",
		},
		{
			// 66 characters, so environment shortens to its first letter and
			// the customer code to its last.
			name:     "at the limit, environment and customer code shorten",
			instance: "environment-db", repo: "hmd-postgres-rds",
			deploymentID: "local", environment: "production", hmdRegion: "us-west-2", customerCode: "customer",
			want: "environment-db_hmd-postgres-rds_local_p_us-west-2_r",
		},
		{
			// Still over after shortening, so it drops to four parts -- which
			// is longer than 64 and stays that way. The fallback is not a
			// length guarantee, it is the last rule.
			name:     "still over after shortening, falls back to four parts",
			instance: "a-very-long-instance-name-here", repo: "an-equally-long-repo-class-name",
			deploymentID: "deployment", environment: "environment", hmdRegion: "us-west-2", customerCode: "customercode",
			want: "a-very-long-instance-name-here_an-equally-long-repo-class-name_deployment_us-west-2",
		},
		{
			name:     "empty environment and customer code are joined as empty",
			instance: "instance", repo: "repo", deploymentID: "did", environment: "", hmdRegion: "reg1", customerCode: "",
			want: "instance_repo_did__reg1_",
		},
		{
			name:     "three long parts alone exceed the limit",
			instance: "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", repo: "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyy",
			deploymentID: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", environment: "env", hmdRegion: "reg1", customerCode: "cc",
			want: "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx_yyyyyyyyyyyyyyyyyyyyyyyyyyyyyy_zzzzzzzzzzzzzzzzzzzzzzzzzzzzzz_reg1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := MakeStandardName(tt.instance, tt.repo, tt.deploymentID, tt.environment, tt.hmdRegion, tt.customerCode)
			if got != tt.want {
				t.Errorf("MakeStandardName()\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// The identifiers are checked against the container names Floci actually
// spawned on a live install, which is the only thing that proves the two
// derivations agree.
func TestResourceIdentifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args [6]string
		want string
	}{
		{
			// Observed as floci-neptune-global-graph-hmd-inf-neptune-local-local-reg1-hmdtr1
			name: "the graph cluster identifier",
			args: [6]string{"global-graph", "hmd-inf-neptune", "local", "local", "reg1", "hmdtr1"},
			want: "global-graph-hmd-inf-neptune-local-local-reg1-hmdtr1",
		},
		{
			name: "the environment database identifier",
			args: [6]string{"environment-db", "hmd-postgres-rds", "local", "local", "reg1", "hmdtr1"},
			want: "environment-db-hmd-postgres-rds-local-local-reg1-hmdtr1",
		},
		{
			name: "uppercase input is lowered",
			args: [6]string{"Instance", "Repo", "DID", "Env", "REG1", "CC"},
			want: "instance-repo-did-env-reg1-cc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := tt.args
			if got := ResourceIdentifier(a[0], a[1], a[2], a[3], a[4], a[5]); got != tt.want {
				t.Errorf("ResourceIdentifier()\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}
