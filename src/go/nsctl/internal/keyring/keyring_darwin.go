package keyring

// HelperName is the macOS keychain client. Always present on a Mac, so
// Available is really asking whether this is a Mac at all.
const HelperName = "security"

// passwordArgs reads a generic password by service and account.
//
// Generic, not internet: keyring's macOS backend stores under
// kSecClassGenericPassword, which is what `find-generic-password` searches.
// Looking in the internet-password class would miss every entry uv wrote.
func passwordArgs(service, username string) []string {
	return []string{"find-generic-password", "-s", service, "-a", username, "-w"}
}

// credentialArgs is nil on macOS, and that is a faithful port rather than an
// omission.
//
// The Python's second leg is keyring.get_credential(service, None). As of
// keyring 25.6.0 the macOS backend does not override get_credential, so the
// base implementation runs -- and it returns None outright whenever username is
// None, without touching the keychain. The leg is therefore dead code on this
// platform, and implementing it here (`find-generic-password -s <service> -w`,
// which does work) would make nsctl resolve credentials hmd-cli-tools cannot.
// Two tools disagreeing about whether a credential exists is worse than one
// lookup fewer.
func credentialArgs(string) []string { return nil }
