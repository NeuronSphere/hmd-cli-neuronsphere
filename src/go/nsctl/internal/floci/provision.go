package floci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
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
// in. Named explicitly rather than relying on Floci's implicit "default" -- see
// EnsureRDSSubnetGroup.
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

type ec2API interface {
	DescribeVpcs(ctx context.Context, in *ec2.DescribeVpcsInput, opts ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	CreateVpc(ctx context.Context, in *ec2.CreateVpcInput, opts ...func(*ec2.Options)) (*ec2.CreateVpcOutput, error)
	DescribeSubnets(ctx context.Context, in *ec2.DescribeSubnetsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	CreateSubnet(ctx context.Context, in *ec2.CreateSubnetInput, opts ...func(*ec2.Options)) (*ec2.CreateSubnetOutput, error)
}

type rdsAPI interface {
	CreateDBSubnetGroup(ctx context.Context, in *rds.CreateDBSubnetGroupInput, opts ...func(*rds.Options)) (*rds.CreateDBSubnetGroupOutput, error)
	DescribeDBInstances(ctx context.Context, in *rds.DescribeDBInstancesInput, opts ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
}

type s3API interface {
	CreateBucket(ctx context.Context, in *s3.CreateBucketInput, opts ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
}

// Provisioner creates the account-level resources a local NeuronSphere needs
// before anything deploys into it.
type Provisioner struct {
	Target  Target
	Secrets secretsAPI
	EC2     ec2API
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
		EC2:     ec2.NewFromConfig(cfg),
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
	// Best-effort, like the Python: on failure the deploy still runs and fails
	// with Floci's own error, which is more informative than one invented here.
	if _, err := p.EnsureRDSSubnetGroup(ctx); err != nil {
		p.warn("could not ensure a DB subnet group for account %s: %v", p.Target.AccountID, err)
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

// EnsureRDSSubnetGroup creates this account's VPC, subnets and DB subnet group.
//
// It works around a Floci multi-account bug. Ec2Service.ensureDefaultResources
// seeds a region's default VPC and subnets but guards on a set keyed by region
// alone, while the storage it writes into is namespaced per account. So the
// first account to touch EC2 in a region marks it seeded and every other
// account is skipped, left with no default VPC at all. RDS then fails
// CreateDBInstance with "InvalidVPCNetworkStateFault: No subnets available for
// DB subnet group default", because its implicit default group is built by
// listing the subnets of vpc-default-<region> in the calling account.
//
// Creating our own VPC cannot fix that path -- Floci resolves the default VPC
// by a fixed id we cannot assign -- so this creates a *named* group instead and
// the local CDKTF overlay places instances in it explicitly, bypassing the
// implicit lookup.
func (p *Provisioner) EnsureRDSSubnetGroup(ctx context.Context) (string, error) {
	vpcID, err := p.ensureVPC(ctx)
	if err != nil {
		return "", err
	}

	subnets, err := p.EC2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}},
	})
	if err != nil {
		return "", fmt.Errorf("describing subnets: %w", err)
	}

	ids := make([]string, 0, len(subnets.Subnets))
	have := map[string]bool{}
	for _, sn := range subnets.Subnets {
		if sn.SubnetId != nil {
			ids = append(ids, *sn.SubnetId)
		}
		if sn.AvailabilityZone != nil {
			have[*sn.AvailabilityZone] = true
		}
	}

	// Two zones: a single-AZ group is enough for these instances, but a
	// multi-AZ one costs nothing and keeps multi_az usable later.
	for _, want := range []struct{ az, cidr string }{
		{p.Target.Region + "a", "172.31.0.0/20"},
		{p.Target.Region + "b", "172.31.16.0/20"},
	} {
		if have[want.az] {
			continue
		}
		created, err := p.EC2.CreateSubnet(ctx, &ec2.CreateSubnetInput{
			VpcId: aws.String(vpcID), CidrBlock: aws.String(want.cidr),
			AvailabilityZone: aws.String(want.az),
		})
		if err != nil {
			p.warn("could not create a subnet in %s: %v", want.az, err)
			continue
		}
		if created.Subnet != nil && created.Subnet.SubnetId != nil {
			ids = append(ids, *created.Subnet.SubnetId)
		}
	}

	if len(ids) == 0 {
		return "", fmt.Errorf("no subnets available in account %s", p.Target.AccountID)
	}

	_, err = p.RDS.CreateDBSubnetGroup(ctx, &rds.CreateDBSubnetGroupInput{
		DBSubnetGroupName:        aws.String(LocalDBSubnetGroup),
		DBSubnetGroupDescription: aws.String("Local NeuronSphere DB subnet group"),
		SubnetIds:                ids,
	})
	if err != nil && !isAPIErrorCode(err, "DBSubnetGroupAlreadyExists") {
		return "", fmt.Errorf("creating the DB subnet group: %w", err)
	}
	return LocalDBSubnetGroup, nil
}

func (p *Provisioner) ensureVPC(ctx context.Context) (string, error) {
	existing, err := p.EC2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{})
	if err != nil {
		return "", fmt.Errorf("describing VPCs: %w", err)
	}
	if len(existing.Vpcs) > 0 && existing.Vpcs[0].VpcId != nil {
		return *existing.Vpcs[0].VpcId, nil
	}
	created, err := p.EC2.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("172.31.0.0/16")})
	if err != nil {
		return "", fmt.Errorf("creating a VPC: %w", err)
	}
	if created.Vpc == nil || created.Vpc.VpcId == nil {
		return "", errors.New("Floci created a VPC with no id")
	}
	return *created.Vpc.VpcId, nil
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
