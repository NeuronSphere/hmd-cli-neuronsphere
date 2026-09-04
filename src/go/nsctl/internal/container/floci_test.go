package container

import "testing"

func TestK3sContainerName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cluster   string
		accountID string
		existing  map[string]bool
		want      string
	}{
		{
			// The live install: account 000000000001, cluster ns-local-57aa833c,
			// container floci-eks-000000000001.ns-local-57aa833c.
			name:    "an environment account qualifies the name",
			cluster: "ns-local-57aa833c", accountID: "000000000001",
			existing: map[string]bool{"floci-eks-000000000001.ns-local-57aa833c": true},
			want:     "floci-eks-000000000001.ns-local-57aa833c",
		},
		{
			name:    "the control-plane account does not qualify",
			cluster: "neuronsphere-57aa833c", accountID: ControlPlaneAccountID,
			existing: nil,
			want:     "floci-eks-neuronsphere-57aa833c",
		},
		{
			name:    "an empty account is treated as the control plane",
			cluster: "some-cluster", accountID: "",
			existing: nil,
			want:     "floci-eks-some-cluster",
		},
		{
			// A cluster created before Floci 2.0 kept the unqualified name and
			// Floci still claims it by label. Resolving by rule alone would
			// point kubeconfig reads at a container that does not exist.
			name:    "a pre-2.0 container keeps its unqualified name",
			cluster: "ns-local-57aa833c", accountID: "000000000001",
			existing: map[string]bool{"floci-eks-ns-local-57aa833c": true},
			want:     "floci-eks-ns-local-57aa833c",
		},
		{
			name:    "neither exists yet, so the qualified name is what will be created",
			cluster: "ns-new-57aa833c", accountID: "000000000002",
			existing: map[string]bool{},
			want:     "floci-eks-000000000002.ns-new-57aa833c",
		},
		{
			// Both present: the qualified name wins, matching Floci's own
			// preference rather than adopting the stale one.
			name:    "the qualified name wins when both exist",
			cluster: "ns-local-57aa833c", accountID: "000000000001",
			existing: map[string]bool{
				"floci-eks-000000000001.ns-local-57aa833c": true,
				"floci-eks-ns-local-57aa833c":              true,
			},
			want: "floci-eks-000000000001.ns-local-57aa833c",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := K3sContainerName(tt.cluster, tt.accountID, tt.existing); got != tt.want {
				t.Errorf("K3sContainerName() = %q, want %q", got, tt.want)
			}
		})
	}
}
