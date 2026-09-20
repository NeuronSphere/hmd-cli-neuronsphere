//go:build !darwin && !linux

package keyring

// HelperName is empty on a platform with no keychain client nsctl knows how to
// drive, which makes Available false and every lookup ErrUnavailable. The
// Python degrades the same way when the keyring module is not installed.
const HelperName = ""

func passwordArgs(string, string) []string { return nil }
func credentialArgs(string) []string       { return nil }
