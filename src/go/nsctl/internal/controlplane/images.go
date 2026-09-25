package controlplane

import (
	"context"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// GUI image resolution, from environments.deployment_gui_image.
const (
	// GUIImageVersion is the version this CLI ships against. A plain pin,
	// because the app is no longer a pre_build_artifacts entry -- it runs from
	// its published image rather than its Helm chart. Neither org tags this
	// image `stable`, so there is no floating alternative to pin to.
	GUIImageVersion = "0.1.1"
	// GUIServiceName is the GUI as everything names it, and GUIRepoClass the
	// RepoClass whose image the control plane runs for it (NERD0015): the
	// core GUI, not the premium overlay that is FROM it. HMD_LOCAL_IMAGE_
	// HMD_APP_NEURONSPHERE names a full reference to run instead -- the
	// premium image, say.
	GUIServiceName       = "hmd-app-neuronsphere"
	GUIRepoClass         = "hmd-app-neuronsphere-core"
	GUIPublishedRegistry = "ghcr.io/hmdlabs"
	// PublishedRegistryDefault is where every other NeuronSphere image is
	// published. An alias for repoclass.PublishedRegistry, which is where it
	// lives so internal/floci can reach it too.
	PublishedRegistryDefault = repoclass.PublishedRegistry
)

// ImagePresent reports whether a ref is already in the host image cache.
type ImagePresent func(ctx context.Context, ref string) bool

// ImageCandidates is every ref the local platform may hold a repo's image
// under, best first. It mirrors image_cache.image_candidates, which is the
// single source of the order floci_deployer.resolve_image_uri resolves in:
//
//  1. $HMD_CONTAINER_REGISTRY/<repo>:<ver> -- what `hmd build` tagged
//  2. $HMD_LOCAL_NS_CONTAINER_REGISTRY/<repo>:<ver>
//  3. each prefix in $HMD_LOCAL_IMAGE_PULL_REGISTRIES
//  4. ghcr.io/hmdlabs/<repo>:<ver> -- the published registry
//  5. bare <repo>:<ver> -- what Floci is handed
//
// Entries whose variable is unset are skipped and duplicates collapse, keeping
// the earliest.
func ImageCandidates(opts *Options, repoName, version string) []string {
	prefixes := []string{
		opts.lookup("HMD_CONTAINER_REGISTRY"),
		opts.lookup("HMD_LOCAL_NS_CONTAINER_REGISTRY"),
	}
	for _, part := range strings.Split(opts.lookup("HMD_LOCAL_IMAGE_PULL_REGISTRIES"), ",") {
		prefixes = append(prefixes, strings.TrimSpace(part))
	}
	prefixes = append(prefixes, PublishedRegistryDefault)

	var candidates []string
	seen := map[string]bool{}
	for _, prefix := range prefixes {
		if prefix == "" {
			continue
		}
		ref := strings.TrimRight(prefix, "/") + "/" + repoName + ":" + version
		if !seen[ref] {
			seen[ref] = true
			candidates = append(candidates, ref)
		}
	}
	return append(candidates, repoName+":"+version)
}

// DeploymentGUIImage is the ref the control-plane compose file runs the
// Deployment GUI from.
//
// A locally cached image wins over a published one -- the same rule
// image_cache.ensure_lambda_image applies to every Lambda -- so `hmd build` in
// the app repo is enough to iterate on the GUI. Nothing cached falls through to
// the published ref, which is then pulled.
//
// The version comes from HMD_LOCAL_VERSION_HMD_APP_NEURONSPHERE_CORE when set,
// so a developer can override the pin; HMD_LOCAL_IMAGE_HMD_APP_NEURONSPHERE is
// a full reference that wins outright (it is how the premium overlay runs
// locally).
func DeploymentGUIImage(ctx context.Context, opts *Options, present ImagePresent) string {
	if ref := opts.lookup(repoclass.ImageEnvVar(GUIServiceName)); ref != "" {
		return ref
	}
	version := opts.lookup(repoclass.VersionEnvVar(GUIRepoClass))
	if version == "" {
		version = GUIImageVersion
	}
	published := GUIPublishedRegistry + "/" + GUIRepoClass + ":" + version

	if present != nil {
		for _, ref := range append(ImageCandidates(opts, GUIRepoClass, version), published) {
			if present(ctx, ref) {
				return ref
			}
		}
	}
	return published
}
