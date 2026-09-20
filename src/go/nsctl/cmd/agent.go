package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/agentskills"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/spf13/cobra"
)

// newAgentCommand installs guidance for an agent that already has nsctl. It
// deliberately does not embed a model or configure an agent host.
func newAgentCommand() *cobra.Command {
	group := &cobra.Command{
		Use:           "agent",
		Short:         "Install bundled skills for coding agents",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(newAgentSkillsCommand())
	return group
}

func newAgentSkillsCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "skills",
		Short: "List, install, remove, and check bundled Agent Skills",
		Long: `Bundled skills teach Codex or Claude Code how to use nsctl; they do not
configure either agent, a model, or an MCP server. Project scope is the default
and writes only under the selected repository.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	group.AddCommand(newAgentSkillsListCommand(), newAgentSkillsInstallCommand(), newAgentSkillsRemoveCommand(), newAgentSkillsDoctorCommand())
	return group
}

type skillFlags struct {
	host  string
	scope string
	path  string
	json  bool
}

func (f *skillFlags) bind(cmd *cobra.Command, jsonOutput bool) {
	cmd.Flags().StringVar(&f.host, "host", "all", "Agent host: codex, claude, or all")
	cmd.Flags().StringVar(&f.scope, "scope", "project", "Install scope: project or user")
	cmd.Flags().StringVar(&f.path, "path", ".", "Project root (not valid with --scope user)")
	if jsonOutput {
		cmd.Flags().BoolVar(&f.json, "json", false, "Print JSON")
	}
}

func (f skillFlags) destinations(names []string) ([]agentskills.Destination, error) {
	scope := agentskills.Scope(f.scope)
	project := f.path
	if scope == agentskills.User {
		if f.path != "." {
			return nil, nserr.New(nserr.Usage, "--path is only valid with --scope project")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nserr.Wrap(nserr.Fail, err)
		}
		return agentskills.Destinations(names, agentskills.Host(f.host), scope, "", home)
	}
	abs, err := filepath.Abs(project)
	if err != nil {
		return nil, nserr.Wrap(nserr.Usage, err)
	}
	return agentskills.Destinations(names, agentskills.Host(f.host), scope, abs, "")
}

func allSkillNames() []string {
	skills := agentskills.List()
	names := make([]string, len(skills))
	for i, skill := range skills {
		names[i] = skill.Name
	}
	return names
}

func newAgentSkillsListCommand() *cobra.Command {
	var flags skillFlags
	cmd := &cobra.Command{
		Use:           "list",
		Short:         "List bundled skills and their state at the selected targets",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dests, err := flags.destinations(allSkillNames())
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if flags.json {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(dests)
			}
			for _, dest := range dests {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", dest.Skill, dest.Description, dest.Host, dest.State, dest.Path)
			}
			return nil
		},
	}
	flags.bind(cmd, true)
	return cmd
}

func newAgentSkillsInstallCommand() *cobra.Command {
	var flags skillFlags
	var force, dryRun bool
	cmd := &cobra.Command{
		Use:           "install <skill> [<skill>...]",
		Short:         "Install selected bundled skills into native agent directories",
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dests, err := flags.destinations(args)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			// Check every destination before changing any of them. An `all`
			// request must not succeed for Codex and then fail silently for Claude.
			if !force {
				for _, dest := range dests {
					if dest.State != agentskills.Missing {
						return nserr.New(nserr.Fail, "%s already exists; use --force to replace it", dest.Path)
					}
				}
			}
			for _, dest := range dests {
				if err := agentskills.Install(dest, force, dryRun); err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				verb := "installed"
				if dryRun {
					verb = "would install"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", verb, dest.Path)
			}
			return nil
		},
	}
	flags.bind(cmd, false)
	cmd.Flags().BoolVar(&force, "force", false, "Replace an existing selected skill")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show destinations without writing")
	return cmd
}

func newAgentSkillsRemoveCommand() *cobra.Command {
	var flags skillFlags
	var force, dryRun bool
	cmd := &cobra.Command{
		Use:           "remove <skill> [<skill>...]",
		Short:         "Remove installed bundled skills",
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dests, err := flags.destinations(args)
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			for _, dest := range dests {
				if err := agentskills.Remove(dest, force, dryRun); err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				verb := "removed"
				if dryRun {
					verb = "would remove"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", verb, dest.Path)
			}
			return nil
		},
	}
	flags.bind(cmd, false)
	cmd.Flags().BoolVar(&force, "force", false, "Remove even if the selected skill changed locally")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show destinations without removing")
	return cmd
}

func newAgentSkillsDoctorCommand() *cobra.Command {
	var flags skillFlags
	cmd := &cobra.Command{
		Use:           "doctor",
		Short:         "Check selected skill locations and installed-skill ownership",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dests, err := flags.destinations(allSkillNames())
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if flags.json {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(dests)
			}
			for _, dest := range dests {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", dest.Skill, dest.Description, dest.Host, dest.State, dest.Path)
			}
			return nil
		},
	}
	flags.bind(cmd, true)
	return cmd
}
