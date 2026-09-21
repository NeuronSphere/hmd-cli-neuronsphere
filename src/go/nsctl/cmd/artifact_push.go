package cmd

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/classart"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
)

// newArtifactPushCommand publishes one RepoClass build zip as an OCI
// artifact. NERD016 SPEC009: the free counterpart of the librarian.
func newArtifactPushCommand(opts *Options) *cobra.Command {
	var token, tag string
	cmd := &cobra.Command{
		Use:   "push <repo-dir | zip> <ref>",
		Short: "Publish a RepoClass build zip to an OCI registry",
		Long: `Publish one RepoClass's build artifact to an OCI registry, where a
neuronsphere.lock entry can name it as its "source" and anyone -- with no
tenant -- can fetch it. A directory is zipped as its manifest declares: the
paths under "license.exclude" stay out, and the "license" SPDX expression
annotates the artifact (org.opencontainers.image.licenses); a zip is pushed
as is and annotated from the manifest inside it. The tag is meta-data/VERSION
unless --tag or the reference names one. A credential is required (--token,
HMD_REGISTRY_TOKEN, or a profile's registry_url after "nsctl login").`,
		Example: `  nsctl artifact push . ghcr.io/acme/classes/hmd-inf-otel-collector --token $GHCR_PAT
  nsctl artifact push dist/hmd-inf-otel-collector_0.1.188_build.zip ghcr.io/acme/classes/hmd-inf-otel-collector`,
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := oci.ParseRef(args[1])
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if ref.Digest != "" {
				return nserr.New(nserr.Usage, "push needs a tag, not a digest: %s", ref)
			}
			cred := registryCredential(opts, ref.Host, token)
			if cred.Anonymous() {
				return nserr.Wrap(nserr.Usage, oci.ErrNoCredential)
			}
			path, err := filepath.Abs(args[0])
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			class, version, manifestJSON, zip, err := classart.FromDir(path)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			switch {
			case tag != "":
				version = tag
			case ref.Tag != "":
				version = ref.Tag
			}
			ref = ref.WithTag(version)
			m, blobs, err := classart.Build(class, version, manifestJSON, zip)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pushing %s@%s to %s with credential from %s\n", class, version, ref, cred.Source)
			fmt.Fprintf(cmd.OutOrStdout(), "Licence: %s\n", licenceLabel(m.Layers[0]))
			d, err := classart.Push(cmd.Context(), oci.New(cred), ref, m, blobs)
			if err != nil {
				return classifyRegistryError(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pushed %s (%s)\nLock entry:  source = %q\n", ref, d, ref.WithTag("").String())
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "registry token or PAT (overrides "+oci.TokenEnv+")")
	cmd.Flags().StringVar(&tag, "tag", "", "tag to publish under (default: meta-data/VERSION)")
	return cmd
}

// pullFromOCI is `artifact pull` for an OCI reference: fetch the class
// artifact, verify, unpack into the cache, and offer it to the local
// librarian best effort (the cache is what resolution reads).
func pullFromOCI(cmd *cobra.Command, opts *Options, libs *librarians, home string, ref oci.Ref, spec string) error {
	client := oci.New(registryCredential(opts, ref.Host, ""))
	ref, err := resolveVersion(cmd.Context(), cmd.OutOrStdout(), client, ref, spec)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Fetching %s (credential: %s)\n", ref, client.Credential.Source)
	got, err := classart.Fetch(cmd.Context(), client, ref)
	if err != nil {
		if errors.Is(err, classart.ErrNotAClass) {
			return nserr.Wrap(nserr.Usage, err)
		}
		return classifyRegistryError(err)
	}
	if err := artifact.Invalidate(home, got.Class, got.Version); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	dir, err := artifact.Store(home, got.Class, got.Version, got.Zip)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	lspec := librarian.Spec{Name: got.Class, Version: got.Version, ItemType: "build"}
	if err := libs.local().Put(cmd.Context(), lspec.ContentPath(), lspec.ItemType, got.Zip); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: the control plane's Artifact Librarian did not accept %s; the cache holds it\n", lspec)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Unpacked %s@%s (%s) to %s\n", got.Class, got.Version, got.Digest, dir)
	return nil
}
