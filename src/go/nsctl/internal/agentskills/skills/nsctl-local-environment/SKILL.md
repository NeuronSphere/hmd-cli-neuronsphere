---
name: nsctl-local-environment
description: Create, run, inspect, and stop a local NeuronSphere environment. Use when a user asks to run, view, or clean up their local NeuronSphere deployment.
---

# Work with a local environment

A container engine and `HMD_HOME` are the only prerequisites: no account,
tenant or token is needed for anything local, and no local command sends a
credential. First establish `HMD_HOME`; `nsctl` does not guess it. Read the
environment list and command help before choosing an environment. Explain the
distinction between the shared control plane and an environment's deployment
state.

Before a create, start, stop, purge, or other mutation, state the target and
the expected effect and obtain confirmation when the user has not already made
that request explicit. Prefer status, plan, and logs for diagnosis; never use
purge or teardown as a first diagnostic action.

# nsctl-command: env
# nsctl-command: control-plane
