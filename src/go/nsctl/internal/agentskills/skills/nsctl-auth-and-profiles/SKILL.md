---
name: nsctl-auth-and-profiles
description: Guide safe login, profile selection, credential checks, and logout for nsctl. Use when a user asks to sign in, change a connection profile, verify identity, or sign out.
---

# Work safely with identity

Use `nsctl whoami` to inspect an existing session and `nsctl login` only when
the user asks to authenticate. Explain the selected profile or authorization
endpoint before changing it. Follow device authorization instructions without
asking the user to paste a token, secret, or browser callback into chat.

`nsctl logout` discards the cached credential. State that consequence and seek
confirmation unless the user explicitly asked to sign out.

# nsctl-command: login
# nsctl-command: logout
# nsctl-command: whoami
