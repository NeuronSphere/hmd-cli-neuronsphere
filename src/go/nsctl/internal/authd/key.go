// Package authd serves a local stand-in for Okta.
//
// The same bargain Floci takes for AWS: emulate the vendor's shape so the
// cloud's own configuration runs locally unmodified. The shape is not a choice
// here -- it is pinned by three consumers that build Okta URLs by string
// concatenation and cannot be asked to do otherwise:
//
//	superset_config.py       OKTA_BASE_URL + "v1/token", + "v1/authorize",
//	                         + "/.well-known/oauth-authorization-server"
//	webserver_config.py      the same, for Airflow
//	hmd_lib_auth             AccessTokenVerifier(issuer) -> {issuer}/v1/keys
//
// so every path below is `{issuer}/v1/...` because that is where those look.
//
// What this is NOT is an authorization server anyone should trust. It signs
// with a key it generated itself and will mint a token with any claims asked
// of it. That is the point -- the thing being tested locally is what the
// platform *does* with a token's claims, and the two places that matter are
// Rego policy and the apps' group-to-role mapping. Neither verifies a
// signature: every Rego bundle in the monorepo calls io.jwt.decode, never
// io.jwt.decode_verify.
package authd

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
)

// keyBits is deliberately modest. This key protects nothing -- it exists so
// that tokens parse and JWKS lookups resolve -- and 2048 generates fast enough
// that a cold `control-plane start` does not visibly pause on it.
const keyBits = 2048

// Key is the signing key and the `kid` that names it in the JWKS.
type Key struct {
	private *rsa.PrivateKey
	id      string
}

// LoadOrCreateKey reads the signing key from dir, generating and persisting one
// if there is none.
//
// Persisted rather than generated per process because the container restarts.
// A new key on every restart invalidates every token already issued, which
// looks like an authentication bug from a browser session that was working a
// moment ago and is nothing of the sort. dir lives under $HMD_HOME, which the
// compose service mounts at its own absolute path.
func LoadOrCreateKey(dir string) (*Key, error) {
	path := filepath.Join(dir, "signing-key.pem")
	if data, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("%s is not PEM; delete it and it will be regenerated", path)
		}
		private, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		return newKey(private), nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	private, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return nil, fmt.Errorf("generating a signing key: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	encoded := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(private),
	})
	// 0600: worthless as a secret, but a world-readable private key in
	// $HMD_HOME is the kind of thing that outlives the reason it was harmless.
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	return newKey(private), nil
}

// newKey derives the `kid` from the public key itself, so the identifier
// changes exactly when the key does and two processes reading the same PEM
// agree without coordinating.
func newKey(private *rsa.PrivateKey) *Key {
	sum := sha256.Sum256(private.PublicKey.N.Bytes())
	return &Key{private: private, id: base64.RawURLEncoding.EncodeToString(sum[:])[:16]}
}

// ID is the `kid` this key signs with and publishes under.
func (k *Key) ID() string { return k.id }

// JWKS is the key set served at {issuer}/v1/keys.
//
// The one endpoint whose shape is not negotiable: okta_jwt_verifier fetches it
// and matches on `kid`, so a set that omits it verifies nothing.
func (k *Key) JWKS() map[string]any {
	return map[string]any{"keys": []any{k.jwk()}}
}

func (k *Key) jwk() map[string]any {
	return map[string]any{
		"kty": "RSA",
		"alg": "RS256",
		"use": "sig",
		"kid": k.id,
		"n":   base64.RawURLEncoding.EncodeToString(k.private.PublicKey.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.private.PublicKey.E)).Bytes()),
	}
}

// Sign returns claims as a compact RS256 JWT.
//
// Hand-rolled rather than pulling in a JWT library, because signing is the only
// direction needed and it is three stdlib calls: the module's direct
// dependencies are the AWS SDK, cobra, docker and yaml, and a fifth earns its
// place by doing something the standard library cannot. Verification is where a
// library would earn it, and nothing here verifies -- the consumers do, in
// Python.
func (k *Key) Sign(claims map[string]any) (string, error) {
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": k.id}
	encodedHeader, err := encodeSegment(header)
	if err != nil {
		return "", err
	}
	encodedClaims, err := encodeSegment(claims)
	if err != nil {
		return "", err
	}
	signingInput := encodedHeader + "." + encodedClaims
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, k.private, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("signing the token: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func encodeSegment(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
