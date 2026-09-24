---
name: nsctl-auth-and-profiles
description: Guide safe login, profile selection, credential checks, and logout for nsctl. Use when a user asks to sign in, change a connection profile, verify identity, or sign out.
---

# Work safely with identity

These verbs exist for what is not local: a hosted NeuronSphere tenant and
private registries. Local environments, deploys and repositories need no
credential, so signing in never fixes a local failure and "not signed in" is the
ordinary state of a working machine -- see `nsctl-debug`.

Use `nsctl whoami` to inspect an existing session and `nsctl login` only when
the user asks to authenticate, or when a command has refused with a named
credential error of its own. Explain the selected profile or authorization
endpoint before changing it. Follow device authorization instructions without
asking the user to paste a token, secret, or browser callback into chat.

`nsctl logout` discards the cached credential. State that consequence and seek
confirmation unless the user explicitly asked to sign out.

# nsctl-command: login
# nsctl-command: logout
# nsctl-command: whoami
