package controlplane

import (
	"context"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dockercreds"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/doctor"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
)

// credentialLookup is dockercreds.Resolver.Lookup, narrowed for tests.
type credentialLookup func(ctx context.Context, server string) (dockercreds.Credential, bool)

// RegistryLoginChecks is the doctor row for a docker login to the private
// image registry.
func RegistryLoginChecks(ctx context.Context) []doctor.Check {
	return registryLoginCheck(ctx, dockercreds.Resolver{}.Lookup)
}

// registryLoginCheck is the pure decision. A warning, not a failure: images
// that are public pull without it, and the platform starts either way.
func registryLoginCheck(ctx context.Context, lookup credentialLookup) []doctor.Check {
	c := doctor.Check{Name: "registry login"}
	if _, ok := lookup(ctx, floci.PrivateImageRegistry); ok {
		c.Status = doctor.StatusOK
		c.Detail = "docker holds a credential for " + floci.PrivateImageRegistry + "; nsctl passes it to the local cluster"
		return []doctor.Check{c}
	}
	c.Status = doctor.StatusWarn
	c.Detail = "docker holds no credential for " + floci.PrivateImageRegistry + ", so the local cluster can pull only " +
		"public images there. A workload whose image is private stays in ImagePullBackOff."
	c.Remedy = "docker login " + floci.PrivateImageRegistry + " -u <github user>   (a token with read:packages as the password),\n" +
		"then `nsctl env start` again so the cluster picks it up."
	return []doctor.Check{c}
}
