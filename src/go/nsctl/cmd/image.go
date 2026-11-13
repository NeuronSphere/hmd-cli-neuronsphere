package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// newImageCommand groups what moves container images into an environment.
func newImageCommand(opts *Options) *cobra.Command {
	imageCmd := &cobra.Command{
		Use:           "image",
		Short:         "Put container images where an environment's pods can run them",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	imageCmd.AddCommand(newImageLoadCommand(opts))
	return imageCmd
}

// newImageLoadCommand copies host images into an environment's cluster.
//
// A transform step runs an image the author built, and that image is usually
// on this machine and nowhere else. The cluster is its own node with its own
// image store, so a tag in the host's Docker is invisible to a pod until it is
// put there; and a registry credential is the wrong way to get it there, since
// a local build was never pushed and a credential in the cluster outlives the
// session that put it in. The host is already signed in as the user, so the
// host does the pulling and the node receives the bytes.
func newImageLoadCommand(opts *Options) *cobra.Command {
	var envName string
	cmd := &cobra.Command{
		Use:   "load <image>...",
		Short: "Load images from this machine's Docker into an environment's cluster",
		Long: `Make each image runnable by pods in the environment's cluster with no registry
pull and no pull secret.

An image already in the host's Docker is used as it is, so a local build wins
over a published image of the same name. One that is not is pulled first, with
whatever the host is signed in as (docker login). Pods then find it in the node
as long as they name the same reference and do not force a pull: Kubernetes
pulls only when the tag is "latest" or the policy says Always.

Loading is idempotent; running it again replaces the node's copy.`,
		Example: `  nsctl image load ghcr.io/hmdlabs/hmd-tf-pdf-generation:0.1.2
  nsctl image load my-transform:dev --env tx`,
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, _, err := loadRegistry(opts)
			if err != nil {
				return err
			}
			env, err := reg.Environment(envName, opts.Lookup)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			ctx := cmd.Context()
			d := container.New()
			if err := d.Available(ctx); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			node := floci.K3sContainerName(env.K3sCluster, env.AccountID, d.ContainerNames(ctx))
			if running, _ := d.Running(ctx, node); !running {
				return nserr.New(nserr.Fail,
					"environment %q has no running cluster (%s); `nsctl env start %s` brings it up", env.Slug, node, env.Slug)
			}

			out := cmd.OutOrStdout()
			for _, ref := range args {
				if !d.ImagePresent(ctx, ref) {
					fmt.Fprintf(out, "pulling %s\n", ref)
					if err := d.PullImage(ctx, ref); err != nil {
						return nserr.New(nserr.Fail,
							"%s is not on this machine and could not be pulled: %v. If it is private, `docker login` to its registry first; if it is a local build, build it first",
							ref, err)
					}
				}
				fmt.Fprintf(out, "loading %s into %s\n", ref, env.Slug)
				if err := d.ImportToNode(ctx, node, ref); err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "Environment to load into (default: the default environment)")
	return cmd
}
