package nsconfig

import (
	"fmt"
	"strings"
)

// Environment variables that name a cloud service outright.
//
// HMD_ARTIFACT_LIBRARIAN_URL is the one the Python tools and every existing
// nsctl install already read; the deployment one is new, and is deliberately
// not HMD_DEPLOYMENT_SERVICE_URL. That name is taken and means the opposite:
// hmd-cli-deploy treats it as the *local* short-circuit, and nsctl sets it on
// every projectbuilder container to point at the control plane's own service
// through hmd_proxy. A cloud address under that name would be read as a local
// one by half the platform.
const (
	ArtifactLibrarianURLEnv = "HMD_ARTIFACT_LIBRARIAN_URL"
	DeploymentURLEnv        = "HMD_CLOUD_MS_DEPLOYMENT_URL"

	CustomerCodeEnv = "HMD_CUSTOMER_CODE"
	RegionEnv       = "HMD_REGION"
)

// Service is one cloud service whose address nsctl has to resolve.
//
// Two of them, and one rule: keeping the rule in one place is the whole reason
// this type exists rather than each client growing its own ladder of
// environment variables.
type Service struct {
	// Label names the service in an error a person reads.
	Label string
	// URLEnv is the environment variable that names its address outright.
	URLEnv string
	// ProfileKey is the TOML key it reads, so a refusal can be acted on by
	// editing the file rather than by reading the documentation.
	ProfileKey string
	// hostPrefix is the leftmost label of the composed hostname.
	hostPrefix string
	// fromProfile reads this service's explicit URL out of a profile.
	fromProfile func(Profile) string
}

// ArtifactLibrarian and Deployment are the two services a profile can address.
//
// The composed hostnames are the platform's own, not this package's invention:
// hmd_lib_librarian_client composes the first, and hmd-cli-ns-bootstrap and
// hmd-cli-deploy both compose the second, all three with "admin" hard-coded.
var (
	ArtifactLibrarian = Service{
		Label:       "Artifact Librarian",
		URLEnv:      ArtifactLibrarianURLEnv,
		ProfileKey:  "artifact_librarian_url",
		hostPrefix:  "artifact",
		fromProfile: func(p Profile) string { return p.ArtifactLibrarianURL },
	}

	Deployment = Service{
		Label:       "deployment service",
		URLEnv:      DeploymentURLEnv,
		ProfileKey:  "deployment_url",
		hostPrefix:  "ms-deployment",
		fromProfile: func(p Profile) string { return p.DeploymentURL },
	}
)

// Endpoint is a resolved address and an account of where it came from.
//
// Source is carried because an address is the thing a credential gets sent to.
// A command that reports which service it is talking to can only do so honestly
// if the resolution said, and "it came from your profile" and "it came from an
// environment variable you forgot was set" are the two answers worth telling
// apart.
type Endpoint struct {
	URL    string
	Source string
	// Shadowed names the profile value an environment variable beat, and is
	// empty when nothing was overridden. The caller decides whether that is
	// worth reporting -- it matters when a profile was named explicitly and is
	// noise when one was merely defaulted to.
	Shadowed string
}

// ResolveEndpoint applies the one precedence rule, in order:
//
//  1. the command's --url flag
//  2. the service's environment variable
//  3. the profile's explicit URL for that service
//  4. composed from customer_code and region -- the profile's first, then
//     HMD_CUSTOMER_CODE and HMD_REGION
//  5. refuse, naming all of the above
//
// An environment variable beating a configuration file is the ordinary
// convention. It is kept, and the one case where it misleads -- a profile that
// names a URL an environment variable then overrides -- is recorded in Shadowed
// rather than being silently correct.
func ResolveEndpoint(s Service, flagURL string, profile Profile, lookup Lookup) (Endpoint, error) {
	if url := strings.TrimSpace(flagURL); url != "" {
		return Endpoint{URL: trimSlash(url), Source: "--url"}, nil
	}

	fromProfile := strings.TrimSpace(s.fromProfile(profile))

	if url := strings.TrimSpace(look(lookup, s.URLEnv)); url != "" {
		e := Endpoint{URL: trimSlash(url), Source: "$" + s.URLEnv}
		if fromProfile != "" && trimSlash(fromProfile) != trimSlash(url) {
			e.Shadowed = trimSlash(fromProfile)
		}
		return e, nil
	}

	if fromProfile != "" {
		return Endpoint{
			URL:    trimSlash(fromProfile),
			Source: fmt.Sprintf("%s in %s", s.ProfileKey, profileLabel(profile)),
		}, nil
	}

	// Composition, profile first. A profile naming a customer and a region is a
	// complete description of a tenant, and having to spell out two long URLs
	// to say the same thing would be a worse one.
	if customer, region := trimmed(profile.CustomerCode), trimmed(profile.Region); customer != "" && region != "" {
		return Endpoint{
			URL:    s.compose(customer, region),
			Source: fmt.Sprintf("customer_code and region in %s", profileLabel(profile)),
		}, nil
	}
	if customer, region := trimmed(look(lookup, CustomerCodeEnv)), trimmed(look(lookup, RegionEnv)); customer != "" && region != "" {
		return Endpoint{
			URL:    s.compose(customer, region),
			Source: fmt.Sprintf("$%s and $%s", CustomerCodeEnv, RegionEnv),
		}, nil
	}

	return Endpoint{}, s.unresolved(profile)
}

// compose builds the per-tenant admin hostname.
func (s Service) compose(customer, region string) string {
	return fmt.Sprintf("https://%s-aaa-%s.%s-admin-neuronsphere.io", s.hostPrefix, region, customer)
}

// unresolved names every tier, because a refusal naming only the last one sends
// the reader to the documentation to find the other four.
func (s Service) unresolved(profile Profile) error {
	return fmt.Errorf("no %s endpoint. It is taken from, in order:\n"+
		"  1. --url\n"+
		"  2. $%s\n"+
		"  3. %s in %s\n"+
		"  4. customer_code and region in that profile, or $%s and $%s\n"+
		"  5. nothing else -- nsctl never guesses an endpoint\n"+
		"Name the tenant once and both services follow:\n%s",
		s.Label, s.URLEnv, s.ProfileKey, profileLabel(profile),
		CustomerCodeEnv, RegionEnv, TenantExample(profile.Name))
}

// TenantExample is a pasteable profile table that resolves both services, used
// by the refusal above. An error that says what is missing without showing the
// shape sends the user to the documentation for four lines of TOML.
func TenantExample(name string) string {
	if name == "" {
		name = "neuronsphere"
	}
	return fmt.Sprintf(`
    [profile.%s]
    auth_url      = "https://auth-aaa-reg1.%s-admin-neuronsphere.io/oauth2/ns"
    customer_code = "%s"
    region        = "reg1"
`, name, name, name)
}

// profileLabel names the profile in prose, and says so plainly when there is
// none -- `in profile ""` is not a sentence anyone can act on.
func profileLabel(p Profile) string {
	if trimmed(p.Name) == "" {
		return "a profile in " + Name
	}
	return fmt.Sprintf("profile %q", p.Name)
}

func look(lookup Lookup, key string) string {
	if lookup == nil {
		return ""
	}
	return lookup(key)
}

func trimmed(s string) string { return strings.TrimSpace(s) }

func trimSlash(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }
