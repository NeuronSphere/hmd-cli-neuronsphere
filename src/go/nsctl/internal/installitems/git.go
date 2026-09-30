package installitems

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// errNoRepoHome is a git item on a machine with no repository folder.
var errNoRepoHome = errors.New("a git-sourced item clones into $HMD_REPO_HOME, which is not set; set it in $HMD_HOME/.config/hmd.env or pass --repo-home")

// cloneName is the directory a git item lands in under the repo folder.
func cloneName(src *Source) string {
	if src.Checkout != "" {
		return src.Checkout
	}
	u := strings.TrimSuffix(strings.TrimRight(src.Git, "/"), ".git")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	return u
}

// clonePath is where src is (or will be) cloned.
func clonePath(repoHome string, src *Source) (string, error) {
	if repoHome == "" {
		return "", errNoRepoHome
	}
	name := cloneName(src)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("cannot name a checkout for %s; give the source a checkout", Redact(src.Git))
	}
	return filepath.Join(repoHome, name), nil
}

// normalizeOrigin reduces a remote URL to what identifies the repository.
func normalizeOrigin(u string) string {
	u = strings.TrimSpace(u)
	u = userinfoRe.ReplaceAllString(u, "$1")
	u = strings.TrimRight(u, "/")
	u = strings.TrimSuffix(u, ".git")
	return strings.ToLower(u)
}

func sameOrigin(a, b string) bool { return normalizeOrigin(a) == normalizeOrigin(b) }

// cloneState reports what is at a git item's target: nothing, a clone of the
// same origin, or something nsctl must not touch.
func cloneState(ctx context.Context, env Env, dest, url string) (exists bool, err error) {
	info, err := os.Stat(dest)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return true, fmt.Errorf("%s exists and is not a directory; it is not a clone of %s", dest, Redact(url))
	}
	git, _ := lookPath("git", env.Environ)
	out, err := exec.CommandContext(ctx, git, "-C", dest, "remote", "get-url", "origin").Output()
	if err != nil {
		return true, fmt.Errorf("%s exists and is not a git clone with an origin; nsctl will not clone over it", dest)
	}
	if !sameOrigin(string(out), url) {
		return true, fmt.Errorf("%s is a clone of %s, not %s; nsctl will not touch it", dest,
			Redact(strings.TrimSpace(string(out))), Redact(url))
	}
	return true, nil
}

// ensureClone clones src into the repo folder unless the same origin is
// already there, which it then leaves exactly as it is: no fetch, pull,
// reset or checkout, because the tree is the user's (SPEC011). created
// reports whether this call made it, so a failed install can undo only that.
func ensureClone(ctx context.Context, env Env, src *Source) (dest string, created bool, err error) {
	dest, err = clonePath(env.RepoHome, src)
	if err != nil {
		return "", false, err
	}
	exists, err := cloneState(ctx, env, dest, src.Git)
	if err != nil {
		return "", false, err
	}
	if exists {
		fmt.Fprintf(env.Stdout, "Using the clone already at %s (left as it is)\n", dest)
		return dest, false, nil
	}
	if err := os.MkdirAll(env.RepoHome, 0o755); err != nil {
		return "", false, err
	}
	git, _ := lookPath("git", env.Environ)
	args := []string{"clone"}
	if src.Ref != "" {
		args = append(args, "--branch", src.Ref)
	}
	args = append(args, src.Git, dest)
	fmt.Fprintf(env.Stdout, "Cloning %s into %s\n", Redact(src.Git), dest)
	if tail, err := run(ctx, env, env.Environ, env.RepoHome, git, args...); err != nil {
		return "", false, fmt.Errorf("git clone %s failed (%v); git used your own credentials and configuration\n%s",
			Redact(src.Git), err, tail)
	}
	return dest, true, nil
}

// inClone joins a root-relative path onto a clone, refusing one that leaves
// it.
func inClone(clone, rel string) (string, error) {
	if rel == "" {
		return clone, nil
	}
	clean := path.Clean("/" + filepath.ToSlash(rel))
	return filepath.Join(clone, filepath.FromSlash(clean)), nil
}
