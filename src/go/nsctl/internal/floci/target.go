// Package floci drives the Floci AWS emulator with aws-sdk-go-v2.
//
// The single most important rule here, from SPEC007: **the access key selects
// the account, and the endpoint does not.** There is exactly one Floci
// container behind the `neuronsphere` alias. The control plane and every
// environment are separate accounts inside it, and Floci resolves which one a
// request belongs to from the SigV4 access key id it is signed with -- a
// 12-digit access key *is* the account.
//
// Signing with the wrong key does not fail. The call succeeds against the wrong
// account. That is why the account is carried on the Target and threaded
// through every credential-building site, and why an ambient AWS_ACCESS_KEY_ID
// is never used.
package floci

import (
	"context"
	"fmt"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hosturl"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// Endpoints and accounts, matching floci_deployer's module constants.
const (
	// InternalEndpoint is the in-network address baked into API Gateway invoke
	// URLs and handed to Lambdas as AWS_ENDPOINT_URL.
	InternalEndpoint = "http://neuronsphere:4566"
	// ControlPlaneAccountID is Floci's default account.
	ControlPlaneAccountID = "000000000000"
	// ContainerName is the one Floci container.
	ContainerName = "floci"
	// Alias is the name that may go on the wire -- an explicit
	// networks.<net>.aliases entry, never a compose service key, because
	// compose registers every service key as an alias on the shared network.
	Alias = "neuronsphere"
	// DefaultRegion is the AWS region Floci is configured for.
	DefaultRegion = "us-west-2"
)

// Target is one Floci account.
//
// Container and Alias stay distinct. Container is the Docker container name --
// for `docker exec` and `docker inspect` and nothing else. Alias is the name
// that may be put on the wire.
type Target struct {
	Name             string
	Endpoint         string
	InternalEndpoint string
	AccountID        string
	Container        string
	Alias            string
	Region           string
	// AccessKeyID is the account selector. It defaults to AccountID and should
	// never be read from the ambient environment.
	AccessKeyID string
	// PollInterval is how often WaitForHealth retries. Zero means the default
	// three seconds, which matches wait_for_floci's cadence; tests set it low.
	PollInterval time.Duration
}

// Lookup resolves an environment variable, returning "" when unset.
type Lookup = func(string) string

// ControlPlane returns the control-plane account: ms-deployment, ms-naming,
// artifact-lib.
func ControlPlane(lookup Lookup) Target {
	return Target{
		Name:             "control-plane",
		Endpoint:         endpoint(lookup),
		InternalEndpoint: InternalEndpoint,
		AccountID:        ControlPlaneAccountID,
		Container:        ContainerName,
		Alias:            Alias,
		Region:           region(lookup),
		AccessKeyID:      ControlPlaneAccountID,
	}
}

// ForAccount returns the target for one environment's account.
//
// Same container, same endpoints and same alias as the control plane -- the
// environment is a distinct account within the single Floci, selected by
// signing with its 12-digit account id.
//
// A legacy-layout environment *is* the control-plane account, so it resolves to
// that target outright.
func ForAccount(lookup Lookup, accountID string, legacyLayout bool) Target {
	if legacyLayout || accountID == "" {
		return ControlPlane(lookup)
	}
	t := ControlPlane(lookup)
	t.Name = "account " + accountID
	t.AccountID = accountID
	t.AccessKeyID = accountID
	return t
}

func endpoint(lookup Lookup) string {
	if lookup != nil {
		if v := lookup("FLOCI_ENDPOINT"); v != "" {
			return v
		}
		if v := lookup("MINISTACK_ENDPOINT"); v != "" {
			return v
		}
	}
	return DefaultEndpoint()
}

// DefaultEndpoint is where hmd_proxy streams the single Floci. The Floci
// container publishes nothing itself.
//
// A function rather than a constant because the port is not fixed: 4566 is also
// LocalStack's, so a machine already running one publishes this somewhere else
// and every URL naming it has to follow (NERD007 SPEC001).
func DefaultEndpoint() string { return hosturl.Floci() }

func region(lookup Lookup) string {
	if lookup != nil {
		if v := lookup("AWS_REGION"); v != "" {
			return v
		}
	}
	return DefaultRegion
}

// Config builds an AWS config pinned to this target's account and endpoint.
//
// The credentials provider carries the account id as the access key. That is
// the account selector, not a dummy value, and it is never read from the
// ambient AWS_ACCESS_KEY_ID -- signing with the wrong key succeeds against the
// wrong account rather than failing, which is the worst possible failure mode.
func (t Target) Config(ctx context.Context) (aws.Config, error) {
	key := t.AccessKeyID
	if key == "" {
		key = t.AccountID
	}
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(t.Region),
		config.WithBaseEndpoint(t.Endpoint),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(key, "dummykey", ""),
		),
		// Nothing here should consult ~/.aws or the environment: the account is
		// the access key, and an inherited profile would silently retarget it.
		config.WithSharedConfigFiles(nil),
		config.WithSharedCredentialsFiles(nil),
	)
	if err != nil {
		return aws.Config{}, fmt.Errorf("building an AWS config for %s: %w", t.Name, err)
	}
	return cfg, nil
}

// WaitForHealth polls Floci's health endpoint until it answers 200.
//
// Ports floci_deployer.wait_for_floci, including its three-second cadence and
// five-minute default: a cold Floci start pulls and boots a Java service, and a
// shorter budget turns a slow first run into a spurious failure.
func (t Target) WaitForHealth(ctx context.Context, timeout time.Duration, progress func(waited, total time.Duration)) error {
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	url := t.Endpoint + "/_floci/health"
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(timeout)

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("building the health request: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Floci at %s is not ready after %s", t.Endpoint, timeout)
		}
		if progress != nil && attempt > 0 && attempt%10 == 0 {
			progress(timeout-time.Until(deadline), timeout)
		}
		interval := t.PollInterval
		if interval == 0 {
			interval = 3 * time.Second
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
