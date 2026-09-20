package floci

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/smithy-go"
)

// apiErr is a service error carrying a code, the shape errors.As matches.
type apiErr struct{ code string }

func (e apiErr) Error() string                 { return e.code }
func (e apiErr) ErrorCode() string             { return e.code }
func (e apiErr) ErrorMessage() string          { return e.code }
func (e apiErr) ErrorFault() smithy.ErrorFault { return smithy.FaultServer }

type fakeSecrets struct {
	existing map[string]bool
	created  map[string]string
	updated  map[string]string
}

func newFakeSecrets(existing ...string) *fakeSecrets {
	f := &fakeSecrets{existing: map[string]bool{}, created: map[string]string{}, updated: map[string]string{}}
	for _, name := range existing {
		f.existing[name] = true
	}
	return f
}

func (f *fakeSecrets) CreateSecret(_ context.Context, in *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	name := aws.ToString(in.Name)
	if f.existing[name] {
		return nil, apiErr{"ResourceExistsException"}
	}
	f.existing[name] = true
	f.created[name] = aws.ToString(in.SecretString)
	return &secretsmanager.CreateSecretOutput{}, nil
}

func (f *fakeSecrets) PutSecretValue(_ context.Context, in *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
	f.updated[aws.ToString(in.SecretId)] = aws.ToString(in.SecretString)
	return &secretsmanager.PutSecretValueOutput{}, nil
}

type fakeS3 struct {
	buckets  map[string]bool
	created  []string
	failWith error
}

func (f *fakeS3) CreateBucket(_ context.Context, in *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	name := aws.ToString(in.Bucket)
	if f.buckets[name] {
		return nil, apiErr{"BucketAlreadyOwnedByYou"}
	}
	if f.buckets == nil {
		f.buckets = map[string]bool{}
	}
	f.buckets[name] = true
	f.created = append(f.created, name)
	return &s3.CreateBucketOutput{}, nil
}

type fakeRDS struct {
	groups        []string
	failWith      error
	status        string
	deleted       []string
	groupExists   bool
	deletedGroups []string

	// deletedClusters records the graph deletes a purge issues. Floci serves
	// Neptune's control plane on the RDS API, which is why they land here.
	deletedClusters []string
}

func (f *fakeRDS) DescribeDBSubnetGroups(_ context.Context, in *rds.DescribeDBSubnetGroupsInput, _ ...func(*rds.Options)) (*rds.DescribeDBSubnetGroupsOutput, error) {
	if !f.groupExists {
		return &rds.DescribeDBSubnetGroupsOutput{}, nil
	}
	return &rds.DescribeDBSubnetGroupsOutput{
		DBSubnetGroups: []rdstypes.DBSubnetGroup{{DBSubnetGroupName: in.DBSubnetGroupName}},
	}, nil
}

func (f *fakeRDS) DeleteDBSubnetGroup(_ context.Context, in *rds.DeleteDBSubnetGroupInput, _ ...func(*rds.Options)) (*rds.DeleteDBSubnetGroupOutput, error) {
	f.deletedGroups = append(f.deletedGroups, aws.ToString(in.DBSubnetGroupName))
	f.groupExists = false
	return &rds.DeleteDBSubnetGroupOutput{}, nil
}

func (f *fakeRDS) DeleteDBCluster(_ context.Context, in *rds.DeleteDBClusterInput, _ ...func(*rds.Options)) (*rds.DeleteDBClusterOutput, error) {
	f.deletedClusters = append(f.deletedClusters, aws.ToString(in.DBClusterIdentifier))
	return &rds.DeleteDBClusterOutput{}, nil
}

func (f *fakeRDS) DeleteDBInstance(_ context.Context, in *rds.DeleteDBInstanceInput, _ ...func(*rds.Options)) (*rds.DeleteDBInstanceOutput, error) {
	f.deleted = append(f.deleted, aws.ToString(in.DBInstanceIdentifier))
	return &rds.DeleteDBInstanceOutput{}, nil
}

func (f *fakeRDS) DescribeDBInstances(_ context.Context, in *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	if f.status == "" {
		return &rds.DescribeDBInstancesOutput{}, nil
	}
	return &rds.DescribeDBInstancesOutput{
		DBInstances: []rdstypes.DBInstance{{DBInstanceStatus: aws.String(f.status)}},
	}, nil
}

func (f *fakeRDS) CreateDBSubnetGroup(_ context.Context, in *rds.CreateDBSubnetGroupInput, _ ...func(*rds.Options)) (*rds.CreateDBSubnetGroupOutput, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	f.groups = append(f.groups, aws.ToString(in.DBSubnetGroupName))
	return &rds.CreateDBSubnetGroupOutput{}, nil
}

func newProvisioner(sec *fakeSecrets, r *fakeRDS, s *fakeS3) *Provisioner {
	return &Provisioner{
		Target:  ForAccount(fakeEnv(nil), "000000000001", false),
		Secrets: sec, RDS: r, S3: s,
		Out: io.Discard, Err: io.Discard,
	}
}

// The admin secret goes under two identities. Both names come from
// make_standard_name and are read back by a Python microservice, so a
// one-character difference fails far from here.
func TestAdminDBSecretIsWrittenUnderBothIdentities(t *testing.T) {
	t.Parallel()

	sec := newFakeSecrets()
	p := newProvisioner(sec, &fakeRDS{}, &fakeS3{})
	names := Names{DeploymentID: "local", Environment: "local", Region: "reg1", CustomerCode: "hmdtr1"}

	if err := p.AdminDBSecret(context.Background(), names, "hmd_db-local"); err != nil {
		t.Fatalf("AdminDBSecret: %v", err)
	}

	want := []string{
		// The container identity every local service configuration expects.
		"hmd_db_hmd-postgres-base_local_local_reg1_hmdtr1_db-secret",
		// The identity a resource-typed database.neuronsphere.io/postgres
		// dependency resolves to.
		"local-neuronsphere_hmd-cli-neuronsphere_local_local_reg1_hmdtr1_db-secret",
	}
	for _, name := range want {
		if _, ok := sec.created[name]; !ok {
			t.Errorf("secret %q was not created; created %v", name, keys(sec.created))
		}
	}
	if len(sec.created) != 2 {
		t.Errorf("created %d secrets, want 2", len(sec.created))
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(sec.created[want[0]]), &payload); err != nil {
		t.Fatalf("the secret is not JSON: %v", err)
	}
	for k, v := range map[string]any{"username": "postgres", "password": "admin", "engine": "aurora-postgresql", "host": "hmd_db-local"} {
		if payload[k] != v {
			t.Errorf("secret[%s] = %v, want %v", k, payload[k], v)
		}
	}
	if payload["port"] != float64(5432) {
		t.Errorf("secret port = %v, want 5432", payload["port"])
	}
}

// An environment's admin secret must carry that environment's slug, because
// the slug is its Environment.type and therefore what ms-deployment passes to a
// consumer's deploy. Hardcoding "local" would name it correctly only in the
// default environment.
func TestAdminDBSecretUsesTheEnvironmentSlug(t *testing.T) {
	t.Parallel()

	sec := newFakeSecrets()
	p := newProvisioner(sec, &fakeRDS{}, &fakeS3{})
	names := Names{DeploymentID: "dev", Environment: "dev", Region: "reg1", CustomerCode: "none"}

	if err := p.AdminDBSecret(context.Background(), names, "hmd_db-dev"); err != nil {
		t.Fatal(err)
	}
	for name := range sec.created {
		if !strings.Contains(name, "_dev_") {
			t.Errorf("secret %q does not carry the environment slug", name)
		}
	}
}

// put_secret_value overwrites, so a second start updates rather than failing.
func TestAdminDBSecretOverwritesAnExistingSecret(t *testing.T) {
	t.Parallel()

	existing := "hmd_db_hmd-postgres-base_local_local_reg1_none_db-secret"
	sec := newFakeSecrets(existing)
	p := newProvisioner(sec, &fakeRDS{}, &fakeS3{})

	if err := p.AdminDBSecret(context.Background(), NamesFrom(fakeEnv(nil), "local", "local"), ""); err != nil {
		t.Fatalf("AdminDBSecret: %v", err)
	}
	if _, ok := sec.updated[existing]; !ok {
		t.Errorf("the existing secret was not updated; updated %v", keys(sec.updated))
	}
}

func TestEnsureBucketToleratesAnExistingBucket(t *testing.T) {
	t.Parallel()

	s := &fakeS3{buckets: map[string]bool{"already": true}}
	p := newProvisioner(newFakeSecrets(), &fakeRDS{}, s)

	if err := p.EnsureBucket(context.Background(), "already"); err != nil {
		t.Errorf("EnsureBucket on an existing bucket: %v", err)
	}
	if err := p.EnsureBucket(context.Background(), "fresh"); err != nil {
		t.Errorf("EnsureBucket: %v", err)
	}
	if len(s.created) != 1 || s.created[0] != "fresh" {
		t.Errorf("created = %v, want just the new bucket", s.created)
	}
}

func TestEnsureBucketReportsARealFailure(t *testing.T) {
	t.Parallel()

	s := &fakeS3{failWith: apiErr{"AccessDenied"}}
	p := newProvisioner(newFakeSecrets(), &fakeRDS{}, s)
	if err := p.EnsureBucket(context.Background(), "b"); err == nil {
		t.Error("EnsureBucket swallowed a real failure")
	}
}

func TestNamesFromReadsTheEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		did  string
		slug string
		want Names
	}{
		{
			name: "defaults",
			want: Names{DeploymentID: "aaa", Environment: "local", Region: "reg1", CustomerCode: "none"},
		},
		{
			name: "from hmd.env",
			env:  map[string]string{"HMD_DID": "bbb", "HMD_REGION": "reg2", "HMD_CUSTOMER_CODE": "hmdtr1"},
			want: Names{DeploymentID: "bbb", Environment: "local", Region: "reg2", CustomerCode: "hmdtr1"},
		},
		{
			name: "an explicit deployment id and slug win",
			env:  map[string]string{"HMD_DID": "bbb"},
			did:  "dev", slug: "dev",
			want: Names{DeploymentID: "dev", Environment: "dev", Region: "reg1", CustomerCode: "none"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NamesFrom(fakeEnv(tt.env), tt.did, tt.slug); got != tt.want {
				t.Errorf("NamesFrom() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// An instance Floci failed to bring back has a record but no container, and
// "no database container" would read as "nothing was ever deployed".
func TestDBInstanceStatusDistinguishesFailedFromAbsent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
		want   string
	}{
		{"no such instance", "", ""},
		{"available", "available", "available"},
		{"failed to recreate its container", "failed", "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProvisioner(newFakeSecrets(), &fakeRDS{status: tt.status}, &fakeS3{})
			if got := p.DBInstanceStatus(context.Background(), "id"); got != tt.want {
				t.Errorf("DBInstanceStatus = %q, want %q", got, tt.want)
			}
		})
	}
}

// A one-time migration for an environment provisioned before hmd-vpc owned the
// network: Terraform has no state for the old group, so its create fails with
// DBSubnetGroupAlreadyExists and the whole substrate deploy stops.
func TestReleaseUnmanagedSubnetGroup(t *testing.T) {
	t.Parallel()

	t.Run("releases a group that is in the way", func(t *testing.T) {
		t.Parallel()
		r := &fakeRDS{groupExists: true}
		p := newProvisioner(newFakeSecrets(), r, &fakeS3{})
		released, err := ReleaseUnmanagedSubnetGroup(context.Background(), p, LocalDBSubnetGroup)
		if err != nil {
			t.Fatalf("ReleaseUnmanagedSubnetGroup: %v", err)
		}
		if !released || len(r.deletedGroups) != 1 {
			t.Errorf("released=%v deleted=%v", released, r.deletedGroups)
		}
	})

	t.Run("does nothing when there is none", func(t *testing.T) {
		t.Parallel()
		r := &fakeRDS{groupExists: false}
		p := newProvisioner(newFakeSecrets(), r, &fakeS3{})
		released, err := ReleaseUnmanagedSubnetGroup(context.Background(), p, LocalDBSubnetGroup)
		if err != nil {
			t.Fatal(err)
		}
		if released || len(r.deletedGroups) != 0 {
			t.Errorf("released=%v deleted=%v, want nothing touched", released, r.deletedGroups)
		}
	})
}
