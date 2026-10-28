---
name: nsctl-session-environment
description: Give this coding session its own local NeuronSphere environment, shaped from a template and the repositories being edited, and keep every deploy and test inside it. Use when a session is about to deploy, test, or iterate against a local environment, or when several sessions share one workstation.
---

# Work in this session's own environment

One session, one environment. Another session on this machine has its own, and
a lease stops either from deploying into the other's. Never take, stop, purge
or deploy into an environment this session does not hold.

Start by asking which environment this session already holds:
`nsctl env lease whoami`. It finds the lease by `NSCTL_LEASE_TOKEN`, or by this
session's own process when a hook took it, and prints the environment, its
template, its repositories and where its routes are served. If it holds one,
use it -- do not acquire another.

If it holds none, take one shaped for the work, naming the template the user
asked for (`nsctl template list` shows them) and the repositories being edited:
`nsctl env lease acquire --session --pool --wait --json --template <t> --repo
<path>`. The command composes the environment, places the session in the
closest free one, and starts it; progress is on stderr and the lease, with its
token, is the JSON on stdout. State the environment and template you were given.

Shell state does not persist between commands here, so exports from `--shell`
are lost. Read the environment and token from `whoami` and pass them on every
command that targets the environment: `HMD_LOCAL_ENV=<env>
NSCTL_LEASE_TOKEN=<token> nsctl env apply`, and the same for `hmd deploy
--local` and `hmd bender`. A refusal naming another holder means the command
went to someone else's environment: stop and fix the target, never pass
`--ignore-lease` or `--steal` without the user asking.

After editing a repository, `nsctl env apply` redeploys what changed. A
refusal because `max_running` environments are already running means wait or
ask the user; never stop another session to make room.

When the work is done, or the user says so, end the session's lease with
`nsctl env lease release --session`; it stops the environment and keeps its
state for the next session. Never purge: `nsctl env purge` is the user's
decision, made when they choose to reclaim space.

# nsctl-command: env lease whoami
# nsctl-command: env lease acquire
# nsctl-command: env lease release
# nsctl-command: env lease heartbeat
# nsctl-command: env apply
# nsctl-command: template list
