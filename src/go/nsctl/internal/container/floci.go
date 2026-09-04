package container

// K3sContainerPrefix is the prefix Floci gives the k3s container it spawns for
// an EKS cluster.
const K3sContainerPrefix = "floci-eks-"

// ControlPlaneAccountID is Floci's default account. Environment accounts are
// allocated above it so an environment can never be mistaken for the control
// plane.
const ControlPlaneAccountID = "000000000000"

// K3sContainerName is the Docker name of the k3s container Floci spawned for a
// cluster, ported from floci_deployer.k3s_container_name.
//
// internal/floci carries the same rule for its own lifecycle helpers; this copy
// exists because internal/status must not import the AWS SDK just to name a
// container.
//
// Floci 2.0 qualifies the name by account for every account except the default
// one, so an environment's cluster is floci-eks-<account>.<cluster> while the
// control plane's stays floci-eks-<cluster>.
//
// The qualified name is checked against what actually exists rather than
// trusted by rule alone, so a cluster created under the pre-2.0 name keeps
// working -- Floci itself claims such a container when its io.floci.account
// label matches. Getting this wrong is not cosmetic: write_kubeconfig reads the
// real kubeconfig with `docker exec` on this name, and on failure falls through
// to a synthesized config with a placeholder token, so every later kubectl call
// fails with "the server has asked for the client to provide credentials" --
// which reads like a broken cluster rather than a naming mistake.
//
// existing is the set of container names on the host; pass ContainerNames.
func K3sContainerName(cluster, accountID string, existing map[string]bool) string {
	legacy := K3sContainerPrefix + cluster
	if accountID == "" || accountID == ControlPlaneAccountID {
		return legacy
	}
	qualified := K3sContainerPrefix + accountID + "." + cluster
	if existing[qualified] {
		return qualified
	}
	if existing[legacy] {
		return legacy
	}
	return qualified
}
