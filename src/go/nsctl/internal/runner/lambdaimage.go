package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// lambdaImageRecord is the file `hmd build` writes next to a built image:
// meta-data/docker.ext_artifact.hmdentity. It names the image the build
// actually produced, registry and org included, so nothing here has to guess
// where a published image lives.
const lambdaImageRecord = "docker.ext_artifact.hmdentity"

type imageRecord struct {
	Name             string `json:"name"`
	ExternalLocation string `json:"external_location"`
}

// stageLambdaImage puts the image a Lambda deploy will run into the host image
// cache, under the bare <name>:<version> tag Floci is handed.
//
// Floci has no ECR. It runs a local Lambda through the host's docker.sock from
// a bare tag (hmd-lib-cdktf-factories' lambda_function, local branch), which is
// unpullable by construction: an unqualified name resolves against Docker Hub.
// In the cloud, `hmd docker deploy` is the step that moves the image from ghcr
// into the account's ECR; its local twin runs inside projectbuilder, which has
// no Docker CLI, so it can only tag what is already cached. This is the host
// side of that step, run where a real Docker exists.
//
// The ref to pull is read from the artifact's own record rather than derived,
// so a pinned artifact names its image exactly. A tree with no record (a
// working copy, which `hmd build` has already cached) or a repo that deploys no
// Lambda image is left alone.
func (r *Runner) stageLambdaImage(ctx context.Context, repoPath string) error {
	if !deploysLambdaImage(repoPath) {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(repoPath, "meta-data", lambdaImageRecord))
	if err != nil {
		return nil
	}
	var rec imageRecord
	if err := json.Unmarshal(raw, &rec); err != nil || rec.Name == "" || rec.ExternalLocation == "" {
		return nil
	}
	tag := imageTag(rec.ExternalLocation)
	if tag == "" {
		return fmt.Errorf("%s names %q, which has no tag", lambdaImageRecord, rec.ExternalLocation)
	}
	bare := rec.Name + ":" + tag

	// A local `hmd build` always wins over a published image.
	if r.imagePresent(ctx, bare) {
		return nil
	}
	if !r.imagePresent(ctx, rec.ExternalLocation) {
		r.step("    pulling %s for the local Lambda", rec.ExternalLocation)
		if _, stderr, err := r.Docker.Run(ctx, "pull", rec.ExternalLocation); err != nil {
			return fmt.Errorf("the Lambda image %s is not in the host image cache and could not be pulled: %s. "+
				"Floci runs local Lambdas from the host cache, so the deploy would succeed and the first request "+
				"would fail with \"pull access denied\". If the package is private, `docker login ghcr.io` first",
				rec.ExternalLocation, strings.TrimSpace(firstNonEmpty(string(stderr), err.Error())))
		}
	}
	if _, stderr, err := r.Docker.Run(ctx, "tag", rec.ExternalLocation, bare); err != nil {
		return fmt.Errorf("tagging %s as %s for Floci: %s", rec.ExternalLocation, bare,
			strings.TrimSpace(firstNonEmpty(string(stderr), err.Error())))
	}
	return nil
}

func (r *Runner) imagePresent(ctx context.Context, ref string) bool {
	_, _, err := r.Docker.Run(ctx, "image", "inspect", "-f", "{{.Id}}", ref)
	return err == nil
}

// imageTag is the tag of a reference, or "" when it has none. A colon before the
// last slash belongs to a registry port, not a tag.
func imageTag(ref string) string {
	i := strings.LastIndex(ref, ":")
	if i < 0 || i < strings.LastIndex(ref, "/") {
		return ""
	}
	return ref[i+1:]
}

// deploysLambdaImage is true when the repo's BACON deploy commands include
// `docker`, which is exactly the set that deploys a Lambda from an image.
// An unreadable manifest is false, leaving the node as it behaved before.
func deploysLambdaImage(repoPath string) bool {
	raw, err := os.ReadFile(filepath.Join(repoPath, "meta-data", "manifest.json"))
	if err != nil {
		return false
	}
	var m struct {
		Deploy struct {
			Commands []json.RawMessage `json:"commands"`
		} `json:"deploy"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	for _, c := range m.Deploy.Commands {
		var name string
		if json.Unmarshal(c, &name) == nil {
			if name == "docker" {
				return true
			}
			continue
		}
		var argv []string
		if json.Unmarshal(c, &argv) == nil && len(argv) > 0 && argv[0] == "docker" {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
