package keyring

// HelperName is the Secret Service client. Unlike macOS's `security` it is not
// always installed; Available reports that, and the caller degrades to "no
// credential" exactly as the Python does when the keyring module is absent.
const HelperName = "secret-tool"

// passwordArgs looks up by the attribute pair keyring's SecretService backend
// writes.
func passwordArgs(service, username string) []string {
	return []string{"lookup", "service", service, "username", username}
}

// credentialArgs is the service-only lookup, which is live on Linux: the
// SecretService backend does override get_credential, searching on the service
// attribute alone. That is what finds a credential uv stored under its own
// account name rather than the index username.
func credentialArgs(service string) []string {
	return []string{"lookup", "service", service}
}
