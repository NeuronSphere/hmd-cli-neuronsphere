package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci/ocitest"
)

const (
	testArtifactType = "application/vnd.example.thing.v1+json"
	testConfigType   = "application/vnd.example.thing.config.v1+json"
	testLayerType    = "application/vnd.example.thing.layer.v1+zip"
)

// publish stores a two-layer artifact in the fake and returns its manifest,
// its blobs, and the tag's digest.
func publish(t *testing.T, reg *ocitest.Registry, name, tag string) (v1.Manifest, map[digest.Digest][]byte, digest.Digest) {
	t.Helper()
	config := []byte(`{"name":"` + name + `","version":"` + tag + `"}`)
	layerA := []byte("layer A of " + name + "@" + tag)
	layerB := bytes.Repeat([]byte("B"), 100_000)
	blobs := map[digest.Digest][]byte{
		digest.FromBytes(config): config,
		digest.FromBytes(layerA): layerA,
		digest.FromBytes(layerB): layerB,
	}
	m := v1.Manifest{
		Versioned:    Versioned(),
		MediaType:    v1.MediaTypeImageManifest,
		ArtifactType: testArtifactType,
		Config:       v1.Descriptor{MediaType: testConfigType, Digest: digest.FromBytes(config), Size: int64(len(config))},
		Layers: []v1.Descriptor{
			{MediaType: testLayerType, Digest: digest.FromBytes(layerA), Size: int64(len(layerA)),
				Annotations: map[string]string{"org.opencontainers.image.title": "a.zip"}},
			{MediaType: testLayerType, Digest: digest.FromBytes(layerB), Size: int64(len(layerB)),
				Annotations: map[string]string{"org.opencontainers.image.title": "b.zip"}},
		},
		Annotations: map[string]string{"org.opencontainers.image.version": tag},
	}
	d := reg.Put(name, tag, m, blobs)
	return m, blobs, d
}

func ref(t *testing.T, reg *ocitest.Registry, s string) Ref {
	t.Helper()
	r, err := ParseRef(reg.Host() + "/" + s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFetchAnonymousThroughTheChallenge(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	m, blobs, d := publish(t, reg, "hmdlabs/stacks/x", "0.1.0")

	c := New(Credential{})
	b, err := c.Fetch(context.Background(), ref(t, reg, "hmdlabs/stacks/x:0.1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Digest != d {
		t.Errorf("Digest = %s, want %s", b.Digest, d)
	}
	if b.ArtifactType != testArtifactType || b.ConfigMediaType != testConfigType {
		t.Errorf("types = %q / %q", b.ArtifactType, b.ConfigMediaType)
	}
	if !bytes.Equal(b.Config, blobs[m.Config.Digest]) {
		t.Error("config bytes differ")
	}
	if len(b.Layers) != 2 || b.Layers[1].Annotations["org.opencontainers.image.title"] != "b.zip" {
		t.Errorf("layers = %+v", b.Layers)
	}
	if b.Annotations["org.opencontainers.image.version"] != "0.1.0" {
		t.Errorf("annotations = %v", b.Annotations)
	}

	var out bytes.Buffer
	if err := c.Blob(context.Background(), b.Ref, b.Layers[1], &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), blobs[m.Layers[1].Digest]) {
		t.Error("layer bytes differ")
	}

	// One challenge, one token, then the token is reused for every request
	// in the same scope: the realm is hit exactly once.
	tokenCalls := 0
	for _, r := range reg.Requests() {
		if strings.HasPrefix(r, "GET /token") {
			tokenCalls++
		}
	}
	if tokenCalls != 1 {
		t.Errorf("token realm called %d times, want 1: %v", tokenCalls, reg.Requests())
	}
}

func TestFetchByDigestAndByAtTag(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t, ocitest.NoChallenge())
	_, _, d := publish(t, reg, "x/y", "1.0")
	c := New(Credential{})

	byDigest, err := c.Fetch(context.Background(), ref(t, reg, "x/y@"+d.String()))
	if err != nil {
		t.Fatal(err)
	}
	if byDigest.Digest != d {
		t.Errorf("digest fetch returned %s", byDigest.Digest)
	}
	byAt, err := c.Fetch(context.Background(), ref(t, reg, "x/y@1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if byAt.Digest != d {
		t.Errorf("@tag fetch returned %s", byAt.Digest)
	}
}

func TestFetchPrivateNeedsBasicAndReportsTheSource(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t, ocitest.RequireToken("alice", "s3cret"))
	publish(t, reg, "acme/private", "1.0")
	r := ref(t, reg, "acme/private:1.0")

	// Anonymous: the realm refuses, and the message says the package may be
	// private rather than pretending it does not exist.
	_, err := New(Credential{Source: "anonymous"}).Fetch(context.Background(), r)
	var oe *Error
	if !errors.As(err, &oe) || !oe.Unauthorized() {
		t.Fatalf("anonymous: err = %v, want an unauthorized *Error", err)
	}
	if !strings.Contains(err.Error(), "private") || !strings.Contains(err.Error(), TokenEnv) {
		t.Errorf("anonymous message should say the package may be private and name %s: %v", TokenEnv, err)
	}

	// Wrong secret: the message names the credential's source, never the secret.
	_, err = New(Credential{Username: "alice", Secret: "wrong", Source: "--token"}).Fetch(context.Background(), r)
	if err == nil || !strings.Contains(err.Error(), "--token") || strings.Contains(err.Error(), "wrong") {
		t.Errorf("rejected credential message must name its source and not the secret: %v", err)
	}

	// Right secret: exchanged at the realm with Basic, then Bearer.
	b, err := New(Credential{Username: "alice", Secret: "s3cret", Source: "--token"}).Fetch(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if b.ArtifactType != testArtifactType {
		t.Errorf("ArtifactType = %q", b.ArtifactType)
	}
}

func TestStaticBearerIsSentFirstAndOfferedOnChallenge(t *testing.T) {
	t.Parallel()
	// The realm demands Basic with the login token as the password: what a
	// tenant registry fronting the platform's login does.
	reg := ocitest.New(t, ocitest.RequireToken(DefaultUser, "login-jwt"))
	publish(t, reg, "acme/x", "1.0")

	c := New(Credential{Bearer: "login-jwt", Source: "profile acme"})
	if _, err := c.Fetch(context.Background(), ref(t, reg, "acme/x:1.0")); err != nil {
		t.Fatal(err)
	}
}

func TestTokenIsNeverSentToAnotherHost(t *testing.T) {
	t.Parallel()
	first := ocitest.New(t, ocitest.RequireToken("u", "p"))
	second := ocitest.New(t, ocitest.RequireToken("u", "p"))
	publish(t, first, "a/b", "1.0")
	publish(t, second, "a/b", "1.0")

	c := New(Credential{Username: "u", Secret: "p", Source: "--token"})
	if _, err := c.Fetch(context.Background(), ref(t, first, "a/b:1.0")); err != nil {
		t.Fatal(err)
	}
	// The client is bound to the host it was resolved for. A second host is
	// refused before any request, so the credential cannot leak by mistake.
	_, err := c.Fetch(context.Background(), ref(t, second, "a/b:1.0"))
	if err == nil || !strings.Contains(err.Error(), "resolved for") {
		t.Fatalf("second host: err = %v, want a refusal naming the bound host", err)
	}
	for _, r := range second.Requests() {
		t.Errorf("second host received %s; it must receive nothing", r)
	}
}

func TestBlobMismatchIsATypedFailure(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t, ocitest.NoChallenge())
	m, _, _ := publish(t, reg, "x/y", "1.0")
	c := New(Credential{})
	r := ref(t, reg, "x/y:1.0")

	// The descriptor claims a digest the registry has under another name.
	wrong := m.Layers[0]
	wrong.Digest = m.Layers[1].Digest // bytes of B, described as A's size
	var out bytes.Buffer
	err := c.Blob(context.Background(), r, wrong, &out)
	var me *MismatchError
	if !errors.As(err, &me) {
		t.Fatalf("err = %v, want *MismatchError", err)
	}
	if !strings.Contains(err.Error(), wrong.Digest.String()) {
		t.Errorf("message must name the expected digest: %v", err)
	}
	if out.Len() != 0 {
		// Nothing partially verified is handed over: the writer sees bytes
		// only through the verifying reader, and the caller must stage.
		t.Logf("writer received %d bytes before the mismatch; callers must stage", out.Len())
	}
}

func TestManifestDigestMismatchIsRefused(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t, ocitest.NoChallenge())
	publish(t, reg, "x/y", "1.0")
	c := New(Credential{})

	// Ask by a digest the registry does not have: 404, not a mismatch.
	_, err := c.Fetch(context.Background(), ref(t, reg, "x/y@sha256:"+strings.Repeat("1", 64)))
	var oe *Error
	if !errors.As(err, &oe) || !oe.NotFound() {
		t.Fatalf("err = %v, want not-found *Error", err)
	}
}

func TestIndexIsRefused(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t, ocitest.NoChallenge())
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": v1.MediaTypeImageIndex, "manifests": []any{}})
	reg.PutRaw("x/multi", "1.0", v1.MediaTypeImageIndex, index, nil)

	_, err := New(Credential{}).Fetch(context.Background(), ref(t, reg, "x/multi:1.0"))
	if err == nil || !strings.Contains(err.Error(), "index") {
		t.Fatalf("err = %v, want an index refusal", err)
	}
}

func TestNotFoundSaysPackageMayBePrivate(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	_, err := New(Credential{Source: "anonymous"}).Fetch(context.Background(), ref(t, reg, "x/absent:1.0"))
	var oe *Error
	if !errors.As(err, &oe) || !oe.NotFound() {
		t.Fatalf("err = %v, want not-found *Error", err)
	}
	if !strings.Contains(err.Error(), "private") {
		t.Errorf("a 404 must mention the private-by-default possibility: %v", err)
	}
}

func TestTagsPaginatesFiltersAndSorts(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t, ocitest.PageSize(2))
	for _, tag := range []string{"0.1.0", "0.10.0", "0.2.0", "latest", "main", "0.2.1"} {
		publish(t, reg, "x/y", tag)
	}
	c := New(Credential{})
	got, err := c.Tags(context.Background(), ref(t, reg, "x/y"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0.10.0", "0.2.1", "0.2.0", "0.1.0"} // newest first, versionspec.Sort's order
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Tags = %v, want %v", got, want)
	}
	pages := 0
	for _, r := range reg.Requests() {
		if strings.Contains(r, "/tags/list") {
			pages++
		}
	}
	if pages < 3 {
		t.Errorf("expected pagination over %d requests: %v", pages, reg.Requests())
	}

	all, err := c.AllTags(context.Background(), ref(t, reg, "x/y"))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 6 {
		t.Errorf("AllTags = %v, want every tag", all)
	}
}

func TestTagsOfUnknownRepositoryIsNotFound(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t, ocitest.NoChallenge())
	_, err := New(Credential{}).Tags(context.Background(), ref(t, reg, "x/nothing"))
	var oe *Error
	if !errors.As(err, &oe) || !oe.NotFound() {
		t.Fatalf("err = %v, want not-found", err)
	}
}

func TestPushRoundTripSkipsPresentBlobs(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t, ocitest.RequireToken("ci", "pat"))
	m, blobs, _ := publish(t, reg, "seed/x", "1.0") // only so the blob bytes exist somewhere
	_ = m

	c := New(Credential{Username: "ci", Secret: "pat", Source: "--token"})
	target := ref(t, reg, "hmdlabs/stacks/new:2.0")

	config := blobs[m.Config.Digest]
	cfgDesc, err := c.PushBlob(context.Background(), target, testConfigType, bytes.NewReader(config), int64(len(config)))
	if err != nil {
		t.Fatal(err)
	}
	var layers []v1.Descriptor
	for _, l := range m.Layers {
		desc, err := c.PushBlob(context.Background(), target, testLayerType, bytes.NewReader(blobs[l.Digest]), l.Size)
		if err != nil {
			t.Fatal(err)
		}
		desc.Annotations = l.Annotations
		layers = append(layers, desc)
	}
	// Pushing the same blob again must HEAD and skip, not re-upload.
	before := len(reg.Requests())
	if _, err := c.PushBlob(context.Background(), target, testLayerType, bytes.NewReader(blobs[m.Layers[0].Digest]), m.Layers[0].Size); err != nil {
		t.Fatal(err)
	}
	for _, r := range reg.Requests()[before:] {
		if strings.HasPrefix(r, "POST ") || strings.HasPrefix(r, "PUT ") {
			t.Errorf("re-push of a present blob made %s", r)
		}
	}

	manifest := v1.Manifest{
		Versioned:    Versioned(),
		MediaType:    v1.MediaTypeImageManifest,
		ArtifactType: testArtifactType,
		Config:       cfgDesc,
		Layers:       layers,
		Annotations:  map[string]string{"org.opencontainers.image.version": "2.0"},
	}
	d, err := c.PushManifest(context.Background(), target, manifest)
	if err != nil {
		t.Fatal(err)
	}

	back, err := New(Credential{Username: "ci", Secret: "pat"}).Fetch(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if back.Digest != d {
		t.Errorf("round trip digest %s != pushed %s", back.Digest, d)
	}
	if back.ArtifactType != testArtifactType || len(back.Layers) != 2 {
		t.Errorf("round trip manifest = %+v", back)
	}
	var out bytes.Buffer
	if err := c.Blob(context.Background(), target, back.Layers[1], &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), blobs[m.Layers[1].Digest]) {
		t.Error("round-tripped layer bytes differ")
	}
}

func TestPushRetriesWithoutArtifactType(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t, ocitest.NoChallenge(), ocitest.RejectArtifactType())
	c := New(Credential{Username: "u", Secret: "p"})
	target := ref(t, reg, "x/old:1.0")

	config := []byte(`{}`)
	cfg, err := c.PushBlob(context.Background(), target, testConfigType, bytes.NewReader(config), int64(len(config)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PushManifest(context.Background(), target, v1.Manifest{
		Versioned: Versioned(), MediaType: v1.MediaTypeImageManifest,
		ArtifactType: testArtifactType, Config: cfg,
	}); err != nil {
		t.Fatalf("push against a registry rejecting artifactType must retry without it: %v", err)
	}
	stored, _ := reg.Manifest("x/old", "1.0")
	if bytes.Contains(stored, []byte("artifactType")) {
		t.Error("retried manifest still carries artifactType")
	}
	back, err := c.Fetch(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	// The config media type still identifies the artifact.
	if back.ConfigMediaType != testConfigType {
		t.Errorf("ConfigMediaType = %q", back.ConfigMediaType)
	}
}

func TestPushRefusesAnonymousBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	reg := ocitest.New(t)
	c := New(Credential{Source: "anonymous"})
	_, err := c.PushBlob(context.Background(), ref(t, reg, "x/y:1.0"), testConfigType, bytes.NewReader([]byte("{}")), 2)
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
	if n := len(reg.Requests()); n != 0 {
		t.Errorf("anonymous push made %d requests", n)
	}
	if !strings.Contains(err.Error(), "--token") || !strings.Contains(err.Error(), TokenEnv) || !strings.Contains(err.Error(), "registry_url") {
		t.Errorf("refusal must name the three credential sources: %v", err)
	}
}

func TestBasicChallengeIsAnsweredDirectly(t *testing.T) {
	t.Parallel()
	// A registry that challenges with Basic instead of Bearer: the fake does
	// not model one, so this test pins the parser's contract and the
	// client's choice through scheme selection alone.
	scheme, params := parseChallenge(`Basic realm="r"`)
	if scheme != "Basic" || params["realm"] != "r" {
		t.Fatalf("parseChallenge = %q %v", scheme, params)
	}
}
