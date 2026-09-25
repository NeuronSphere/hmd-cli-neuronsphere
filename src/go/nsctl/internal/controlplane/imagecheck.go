package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/compose"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/doctor"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hmdenv"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// imageCheckTimeout keeps `nsctl doctor` a diagnostic rather than a wait. The
// registry either answers quickly or is treated as unreachable, which is a
// warning and not a verdict.
const imageCheckTimeout = 10 * time.Second

// ImageChecks reports whether the registry this home is configured for actually
// publishes the image references the control plane pins.
//
// The point is when it runs, not what it does. These references are resolved by
// Floci at instance-creation time, so a tag that exists nowhere is not reported
// as a configuration error: Floci answers CreateDBInstance with a 404 from the
// daemon, leaves the instance `failed`, and Terraform polls "Still creating..."
// until someone kills it. Asked here, before anything is pulled, it is a
// ten-second answer instead of a ten-minute mystery -- and quickstart gates on
// doctor, so a first run stops at the right place.
//
// Read off the interpolated project rather than reconstructed, so the question
// is about exactly what a start would create.
func ImageChecks(ctx context.Context, opts *Options) []doctor.Check {
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil {
		return nil
	}
	project, err := Project(opts, reg, "")
	if err != nil {
		return nil
	}
	var refs []string
	for _, s := range project.Services {
		if s.Key != compose.FlociService {
			continue
		}
		for _, key := range []string{
			"FLOCI_SERVICES_RDS_DEFAULT_POSTGRES_IMAGE",
			"FLOCI_SERVICES_NEPTUNE_DEFAULT_IMAGE",
			"FLOCI_SERVICES_EKS_DEFAULT_IMAGE",
		} {
			if ref := s.Environment[key]; ref != "" {
				refs = append(refs, ref)
			}
		}
	}
	if len(refs) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, imageCheckTimeout)
	defer cancel()
	client := oci.New(oci.Credential{})
	client.HTTP = &http.Client{Timeout: imageCheckTimeout}

	value, origin := hmdenv.OriginOf(opts.Home, opts.Lookup, floci.RegistryEnv)
	checks := make([]doctor.Check, 0, len(refs))
	// One override explains every row, so only the first failure carries the
	// whole remedy. Repeating five lines of it under each of three images
	// buries the three different things that are actually wrong.
	explained := false
	for _, ref := range refs {
		c := imageCheck(ctx, client, ref, value, origin)
		if c.Status == doctor.StatusFail && value != "" && origin != hmdenv.OriginDefault {
			if explained {
				c.Remedy = "same " + floci.RegistryEnv + " setting as above"
			} else {
				explained = true
			}
		}
		checks = append(checks, c)
	}
	return checks
}

// tagLister is the one thing imageCheck needs from a registry, so the decision
// can be tested without one.
type tagLister interface {
	AllTags(ctx context.Context, ref oci.Ref) ([]string, error)
}

func imageCheck(ctx context.Context, client tagLister, ref, override, origin string) doctor.Check {
	c := doctor.Check{Name: "image " + shortName(ref), Detail: ref}

	parsed, err := oci.ParseRef(ref)
	if err != nil {
		c.Status = doctor.StatusWarn
		c.Detail = fmt.Sprintf("%s could not be read as a reference: %v", ref, err)
		return c
	}

	tags, err := client.AllTags(ctx, parsed)
	if err != nil {
		var refusal *oci.Error
		switch {
		case errors.As(err, &refusal) && (refusal.Unauthorized() || refusal.NotFound()):
			// Both shapes mean the same thing from outside: ghcr.io packages
			// are private by default, so a private repository is
			// indistinguishable from an absent one without a credential.
			c.Status = doctor.StatusFail
			c.Detail = fmt.Sprintf("%s is not readable: %v", ref, err)
			c.Remedy = remedy(override, origin)
		default:
			// Offline is not misconfigured.
			c.Status = doctor.StatusWarn
			c.Detail = fmt.Sprintf("could not ask the registry about %s: %v", ref, err)
		}
		return c
	}

	// A digest-pinned reference is not a tag, and listing tags says nothing
	// about it. Nothing here pins by digest today; not guessing is cheaper
	// than a row that is wrong the day something does.
	if parsed.Tag == "" {
		c.Status = doctor.StatusOK
		return c
	}
	for _, tag := range tags {
		if tag == parsed.Tag {
			c.Status = doctor.StatusOK
			return c
		}
	}
	c.Status = doctor.StatusFail
	c.Detail = fmt.Sprintf("%s does not exist: the repository publishes %s", ref, listTags(tags))
	c.Remedy = remedy(override, origin)
	return c
}

// remedy names the override when there is one, because that is the setting a
// reader can change, and says where it was set, because that decides how.
func remedy(override, origin string) string {
	if override == "" || origin == hmdenv.OriginDefault {
		return "pin a tag that exists, or set " + floci.RegistryEnv + " to a registry that publishes one"
	}
	r := fmt.Sprintf("%s=%s, set in %s. nsctl's own default is %s, which is where these images are built and published. Unset the variable, or set it to %s",
		floci.RegistryEnv, override, origin, repoclass.PublishedRegistry, repoclass.PublishedRegistry)
	if origin == hmdenv.OriginShell {
		r += ".\n  " + floci.DoNotDeleteHome
	}
	return r
}

// shortName is the repository's last path element, which is what distinguishes
// one row from another; the full reference is the detail.
func shortName(ref string) string {
	name := ref
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// listTags keeps the row readable when a repository has hundreds.
func listTags(tags []string) string {
	if len(tags) == 0 {
		return "no tags at all"
	}
	sorted := append([]string(nil), tags...)
	sort.Strings(sorted)
	if len(sorted) > 8 {
		return strings.Join(sorted[len(sorted)-8:], ", ") + " (and others)"
	}
	return strings.Join(sorted, ", ")
}

// StorageChecks proves the control plane's object store the same way a start
// does, and reports rather than refuses.
//
// The row exists because the failure it finds is invisible everywhere else:
// Floci answers, its containers are up, `nsctl env status` is green, and the
// only symptom is a deploy dying in `tofu init`. Someone running doctor because
// "something is wrong" should be told here.
func StorageChecks(ctx context.Context, opts *Options) []doctor.Check {
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil || reg.Synthesized || !reg.ControlPlane.Bootstrapped {
		// Nothing has been brought up here, so there is no store to prove and
		// nothing to report about one.
		return nil
	}
	target := floci.ControlPlane(opts.Lookup)
	names := floci.NamesFrom(opts.Lookup, "", "local")

	ctx, cancel := context.WithTimeout(ctx, imageCheckTimeout)
	defer cancel()

	prov, err := floci.NewProvisioner(ctx, target, nil, nil)
	if err != nil {
		return nil
	}
	bucket := prov.TFStateBucket(names.Region)
	c := doctor.Check{Name: "Floci storage", Detail: bucket}
	if err := prov.CheckStorage(ctx, bucket); err != nil {
		c.Status = doctor.StatusWarn
		c.Detail = err.Error()
		c.Remedy = "`nsctl control-plane start` recreates Floci when its data directory is the problem"
		return []doctor.Check{c}
	}
	c.Status = doctor.StatusOK
	c.Detail = "reads back what it writes (" + bucket + ")"
	return []doctor.Check{c}
}
