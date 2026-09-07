package floci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/smithy-go"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tools"
)

// Identities the admin database secret is written under. See AdminDBSecret.
const (
	dbInstanceName = "hmd_db"
	dbRepoClass    = "hmd-postgres-base"
	// CoreInstanceName and CoreRepoClass are bom_seeder's CORE_INSTANCE_NAME
	// and CORE_REPO_CLASS: the identity a resource-typed
	// database.neuronsphere.io/postgres dependency resolves to locally.
	CoreInstanceName = "local-neuronsphere"
	CoreRepoClass    = "hmd-cli-neuronsphere"
)

// LocalDBSubnetGroup is the DB subnet group every local RDS instance is placed
// in, named explicitly rather than relying on Floci's implicit "default" --
// which is built by listing the subnets of vpc-default-<region> in the calling
// account, something Floci seeds only for the first account to touch EC2.
//
// It is created by the base-vpc deploy now, not here. nsctl used to create it
// along with a VPC and subnets through the EC2 API; that workaround moved into
// hmd-vpc's local overlay, where it belongs, and took the EC2 SDK -- 24 MB of
// binary for four calls -- with it.
const LocalDBSubnetGroup = "hmd-local-db-subnets"

// DefaultCustomerCode is the fallback when hmd.env does not set one.
const DefaultCustomerCode = "none"

// Names carries the identity components every standard name is built from.
type Names struct {
	// DeploymentID is HMD_DID, or an environment's deployment_id.
	DeploymentID string
	// Environment is the environment's slug, which is its Environment.type and
	// therefore what ms-deployment passes to a consumer's deploy as
	// --environment. Hardcoding "local" would name the admin secret correctly
	// only in the default environment.
	Environment string
	Region      string
	// CustomerCode must match what the ms-dbaccount Lambda deploys with, or the
	// admin secret is seeded under a name the consumer never looks up and every
	// hmd-database-account deploy fails with ResourceNotFoundException.
	CustomerCode string
}

// NamesFrom reads the identity components out of the environment.
func NamesFrom(lookup Lookup, deploymentID, environment string) Names {
	get := func(key, fallback string) string {
		if lookup != nil {
			if v := lookup(key); v != "" {
				return v
			}
		}
		return fallback
	}
	if deploymentID == "" {
		deploymentID = get("HMD_DID", "aaa")
	}
	if environment == "" {
		environment = "local"
	}
	return Names{
		DeploymentID: deploymentID,
		Environment:  environment,
		Region:       get("HMD_REGION", "reg1"),
		CustomerCode: get("HMD_CUSTOMER_CODE", DefaultCustomerCode),
	}
}

// secretsAPI, ec2API, rdsAPI and s3API narrow the SDK clients to the calls used
// here, so provisioning is testable without a Floci.
type secretsAPI interface {
	CreateSecret(ctx context.Context, in *secretsmanager.CreateSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
	PutSecretValue(ctx context.Context, in *secretsmanager.PutSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
}

type rdsAPI interface {
	CreateDBSubnetGroup(ctx context.Context, in *rds.CreateDBSubnetGroupInput, opts ...func(*rds.Options)) (*rds.CreateDBSubnetGroupOutput, error)
	DescribeDBInstances(ctx context.Context, in *rds.DescribeDBInstancesInput, opts ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
	DeleteDBInstance(ctx context.Context, in *rds.DeleteDBInstanceInput, opts ...func(*rds.Options)) (*rds.DeleteDBInstanceOutput, error)
	DescribeDBSubnetGroups(ctx context.Context, in *rds.DescribeDBSubnetGroupsInput, opts ...func(*rds.Options)) (*rds.DescribeDBSubnetGroupsOutput, error)
	DeleteDBSubnetGroup(ctx context.Context, in *rds.DeleteDBSubnetGroupInput, opts ...func(*rds.Options)) (*rds.DeleteDBSubnetGroupOutput, error)
	DeleteDBCluster(ctx context.Context, in *rds.DeleteDBClusterInput, opts ...func(*rds.Options)) (*rds.DeleteDBClusterOutput, error)
}

type s3API interface {
	CreateBucket(ctx context.Context, in *s3.CreateBucketInput, opts ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
}

// Provisioner creates the account-level resources a local NeuronSphere needs
// before anything deploys into it.
type Provisioner struct {
	Target  Target
	Secrets secretsAPI
	RDS     rdsAPI
	S3      s3API
	// Out carries progress lines; Err carries warnings.
	Out io.Writer
	Err io.Writer
}

// NewProvisioner builds a Provisioner from a target's AWS config.
func NewProvisioner(ctx context.Context, t Target, out, errOut io.Writer) (*Provisioner, error) {
	cfg, err := t.Config(ctx)
	if err != nil {
		return nil, err
	}
	return &Provisioner{
		Target:  t,
		Secrets: secretsmanager.NewFromConfig(cfg),
		RDS:     rds.NewFromConfig(cfg),
		// Floci serves S3 path-style; virtual-host addressing would resolve
		// <bucket>.neuronsphere, which nothing on this network answers for.
		S3:  s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true }),
		Out: out, Err: errOut,
	}, nil
}

// Provision creates this account's admin database secret, its DB subnet group
// and its CDKTF state bucket. Every step is idempotent.
//
// The plugin-declared SQS queues and S3 buckets the Python provisions here come
// from RepoClass declarations now, so they arrive with the BOM rather than from
// a parallel inventory.
func (p *Provisioner) Provision(ctx context.Context, names Names, dbHost string) error {
	if err := p.AdminDBSecret(ctx, names, dbHost); err != nil {
		return err
	}
	return p.EnsureBucket(ctx, p.TFStateBucket(names.Region))
}

// TFStateBucket is the CDKTF S3 backend `hmd cdktf deploy` runs `tofu init`
// against. The name uses the HMD region, matching HmdCdkTfStack's backend, not
// the cloud LocationConstraint region.
func (p *Provisioner) TFStateBucket(hmdRegion string) string {
	return fmt.Sprintf("hmd.%s.%s.tfstate", p.Target.AccountID, hmdRegion)
}

// AdminDBSecret writes the local postgres admin secret under both identities
// ms-dbaccount may look it up by.
//
// hmd-ms-dbaccount reads its admin credentials from a secret named
// `{secret_base}_db-secret`. Two identities point at the same shared Postgres:
//
//  1. hmd_db / hmd-postgres-base / did -- the container identity every local
//     service configuration expects.
//  2. the core instance / hmd-cli-neuronsphere / slug -- the identity a
//     resource-typed database.neuronsphere.io/postgres dependency resolves to.
//     A consumer chart derives its secret name from this one, so without it
//     ms-dbaccount cannot find the admin credentials when a database account
//     deploys under the core identity.
func (p *Provisioner) AdminDBSecret(ctx context.Context, names Names, dbHost string) error {
	if dbHost == "" {
		dbHost = "hmd_db"
	}
	value, err := json.Marshal(map[string]any{
		"username": "postgres",
		"password": "admin",
		"engine":   "aurora-postgresql",
		"host":     dbHost,
		"port":     5432,
	})
	if err != nil {
		return fmt.Errorf("encoding the admin secret: %w", err)
	}

	bases := []string{
		// The RepoInstance name stays hmd_db in every environment:
		// repo_instance is unique by name per Environment, so it is not
		// ambiguous.
		tools.MakeStandardName(dbInstanceName, dbRepoClass, names.DeploymentID, names.Environment, names.Region, names.CustomerCode),
		tools.MakeStandardName(CoreInstanceName, CoreRepoClass, names.Environment, names.Environment, names.Region, names.CustomerCode),
	}
	for _, base := range bases {
		if err := p.putSecret(ctx, base+"_db-secret", string(value)); err != nil {
			return err
		}
	}
	return nil
}

// OktaSecret writes the `okta` secret the local identity provider stands behind.
//
// One secret, read by everything: hmd_lib_auth.verify_token resolves
// `services_issuer` and `ns_issuer` from it to pick which authorization server
// a token should have come from, and the Superset and Airflow charts render
// OKTA_NS_ISSUER from it. Writing it here rather than letting each consumer
// guess keeps the issuer a single string -- which is the property the whole
// arrangement depends on, since a consumer that fetched keys under one name and
// reads `iss` as another rejects every token.
//
// `ns_client_id` is the trusted client every web application's secret lists;
// there is no registry locally, so it is a name rather than an opaque id.
func (p *Provisioner) OktaSecret(ctx context.Context, nsIssuer, servicesIssuer, nsClientID string) error {
	value, err := json.Marshal(map[string]any{
		"ns_issuer":       nsIssuer,
		"services_issuer": servicesIssuer,
		"ns_client_id":    nsClientID,
	})
	if err != nil {
		return fmt.Errorf("encoding the okta secret: %w", err)
	}
	return p.putSecret(ctx, "okta", string(value))
}

// putSecret creates the secret, or overwrites it when it already exists.
func (p *Provisioner) putSecret(ctx context.Context, name, value string) error {
	_, err := p.Secrets.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name: aws.String(name), SecretString: aws.String(value),
	})
	if err == nil {
		return nil
	}
	if !isAPIErrorCode(err, "ResourceExistsException") {
		return fmt.Errorf("creating the secret %s: %w", name, err)
	}
	if _, err := p.Secrets.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId: aws.String(name), SecretString: aws.String(value),
	}); err != nil {
		return fmt.Errorf("updating the secret %s: %w", name, err)
	}
	return nil
}

// EnsureBucket creates an S3 bucket, tolerating one that already exists.
func (p *Provisioner) EnsureBucket(ctx context.Context, name string) error {
	_, err := p.S3.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(name),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(p.Target.Region),
		},
	})
	if err == nil || isAPIErrorCode(err, "BucketAlreadyOwnedByYou", "BucketAlreadyExists") {
		return nil
	}
	return fmt.Errorf("creating the bucket %s: %w", name, err)
}

func (p *Provisioner) warn(format string, a ...any) {
	if p.Err == nil {
		return
	}
	fmt.Fprintf(p.Err, "warning: "+format+"\n", a...)
}

// isAPIErrorCode reports whether err is a service error with one of these
// codes.
//
// errors.As on smithy.APIError, not string-matching a response dict: the code
// is a typed field, and matching text breaks the first time a message is
// reworded.
func isAPIErrorCode(err error, codes ...string) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, code := range codes {
		if apiErr.ErrorCode() == code {
			return true
		}
	}
	return false
}

// DBInstanceStatus is what Floci reports for an RDS instance, or "" when there
// is no such instance.
//
// Worth asking separately from "is there a container": an instance Floci failed
// to bring back reports `failed` while having no container at all, and the two
// need different words. "No database container" reads as "nothing was ever
// deployed"; `failed` means the record is there and its backing container could
// not be recreated, which only a redeploy fixes.
func (p *Provisioner) DBInstanceStatus(ctx context.Context, identifier string) string {
	out, err := p.RDS.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(identifier),
	})
	if err != nil || len(out.DBInstances) == 0 {
		return ""
	}
	return aws.ToString(out.DBInstances[0].DBInstanceStatus)
}

// DeleteDBInstance removes an RDS instance record.
//
// FLOCI_STORAGE_PRUNE_VOLUMES_ON_DELETE is pinned false, so the instance's
// volume survives and a recreated instance of the same identifier can reattach
// to its data. That is what makes clearing a `failed` record a repair rather
// than a loss.
func (p *Provisioner) DeleteDBInstance(ctx context.Context, identifier string) error {
	_, err := p.RDS.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier: aws.String(identifier),
		SkipFinalSnapshot:    aws.Bool(true),
	})
	if err != nil && !isAPIErrorCode(err, "DBInstanceNotFound") {
		return fmt.Errorf("deleting %s: %w", identifier, err)
	}
	return nil
}

// DBSubnetGroupExists reports whether a DB subnet group is present.
func (p *Provisioner) DBSubnetGroupExists(ctx context.Context, name string) bool {
	out, err := p.RDS.DescribeDBSubnetGroups(ctx, &rds.DescribeDBSubnetGroupsInput{
		DBSubnetGroupName: aws.String(name),
	})
	return err == nil && len(out.DBSubnetGroups) > 0
}

// DeleteDBSubnetGroup removes a DB subnet group.
func (p *Provisioner) DeleteDBSubnetGroup(ctx context.Context, name string) error {
	_, err := p.RDS.DeleteDBSubnetGroup(ctx, &rds.DeleteDBSubnetGroupInput{
		DBSubnetGroupName: aws.String(name),
	})
	if err != nil && !isAPIErrorCode(err, "DBSubnetGroupNotFoundFault") {
		return err
	}
	return nil
}

// DeleteDBCluster removes a DB cluster record -- in practice the graph, since
// Floci serves Neptune's control plane on the RDS API.
//
// The Python reaches this through boto3's `neptune` client, which is the same
// operation signed the same way: Neptune's endpoint prefix and signing name are
// both `rds`. So this needs no second SDK client, only the one call.
//
// Unlike DeleteDBInstance, this is data loss and not a repair. Floci mounts no
// volume for a graph -- it lives in the container's writable layer -- so
// removing the container is the deletion, and only a purge should ask.
func (p *Provisioner) DeleteDBCluster(ctx context.Context, identifier string) error {
	_, err := p.RDS.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
		DBClusterIdentifier: aws.String(identifier),
		SkipFinalSnapshot:   aws.Bool(true),
	})
	if err != nil && !isAPIErrorCode(err, "DBClusterNotFoundFault", "ResourceNotFoundException") {
		return fmt.Errorf("deleting the cluster %s: %w", identifier, err)
	}
	return nil
}
