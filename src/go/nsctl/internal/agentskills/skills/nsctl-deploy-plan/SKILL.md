---
name: nsctl-deploy-plan
description: Inspect local deployment inputs and effects before starting work. Use when a user asks what a NeuronSphere deploy will do, wants a deployment plan, or wants to start a local deployment.
---

# Plan a deployment

Identify the environment, declared repository instances, BOM inputs, and lock
state before starting work. Use read-only plan or status commands first and
explain which RepoClasses and dependencies will be affected.

Do not turn a plan request into a deploy. Immediately before a command that
starts, applies, or changes a deployment, summarize the target and obtain the
user's confirmation unless their request already unambiguously authorizes that
exact action.

# nsctl-command: env
# nsctl-command: repo
# nsctl-command: bom
# nsctl-command: lock
