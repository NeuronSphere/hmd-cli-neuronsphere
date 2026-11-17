package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/credentials"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/data"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

// storage and land (NERD039 SPEC009): opt-in, and always a local
// environment's S3. Every environment is an account of the one Floci, chosen
// by the access key, and reached from the host on the forwarded port and from
// containers on the control plane's network.

// StorageBucketOutput is the hmd-inf-s3bucket output that names its bucket.
const StorageBucketOutput = "bucket_name"

// envStorage is one environment's S3, as data needs it.
type envStorage struct {
	Env     string
	env     *registry.Environment
	Target  floci.Target
	Network string
}

// storageOf resolves an environment's S3 and checks that it answers.
func (d *dataCtx) storageOf(ctx context.Context, name string) (*envStorage, error) {
	reg, _, err := loadRegistry(d.opts)
	if err != nil {
		return nil, err
	}
	env, err := reg.Environment(name, d.opts.Lookup)
	if err != nil {
		return nil, nserr.New(nserr.Usage, "no environment %q: %v. `nsctl env list` shows them; `nsctl env add <name>` makes one", name, err)
	}
	s := &envStorage{Env: name, env: env, Target: floci.ForAccount(d.opts.Lookup, env.AccountID, env.LegacyLayout),
		Network: reg.ControlPlane.Network}
	hc := &http.Client{Timeout: 3 * time.Second}
	ctx3, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx3, http.MethodGet, s.Target.Endpoint+"/_floci/health", nil)
	resp, err := hc.Do(req)
	if err == nil {
		resp.Body.Close()
	}
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, nserr.New(nserr.Fail, "environment %s's storage does not answer at %s. Start it with `nsctl env start %s`", name, s.Target.Endpoint, name)
	}
	return s, nil
}

func (s *envStorage) accessKey() string {
	if s.Target.AccessKeyID != "" {
		return s.Target.AccessKeyID
	}
	return s.Target.AccountID
}

// remote is how the engine reaches this S3 from the environment's network.
func (s *envStorage) remote() *data.Remote {
	return &data.Remote{Network: s.Network, Endpoint: strings.TrimPrefix(floci.InternalEndpoint, "http://"),
		Region: s.Target.Region, KeyID: s.accessKey(), Secret: floci.SecretAccessKey}
}

func (s *envStorage) client(ctx context.Context) (*s3.Client, error) {
	cfg, err := s.Target.Config(ctx)
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true }), nil
}

func (s *envStorage) buckets(ctx context.Context) ([]string, error) {
	c, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := c.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, nserr.New(nserr.Fail, "listing environment %s's buckets: %v", s.Env, err)
	}
	var names []string
	for _, b := range out.Buckets {
		names = append(names, aws.ToString(b.Name))
	}
	sort.Strings(names)
	return names, nil
}

// bucketExists answers with HeadBucket, from the host.
func (s *envStorage) bucketExists(ctx context.Context, bucket string) (bool, error) {
	c, err := s.client(ctx)
	if err != nil {
		return false, err
	}
	if _, err := c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)}); err != nil {
		var nf interface{ ErrorCode() string }
		if errors.As(err, &nf) && (nf.ErrorCode() == "NotFound" || nf.ErrorCode() == "NoSuchBucket") {
			return false, nil
		}
		return false, nserr.New(nserr.Fail, "checking bucket %s in environment %s: %v", bucket, s.Env, err)
	}
	return true, nil
}

// resolveTarget turns --to into a storage target. An instance's bucket is
// read from what it produced, never typed.
func (s *envStorage) resolveTarget(ctx context.Context, to string) (*data.Storage, error) {
	bucket, prefix, instance, err := data.ParseTarget(to)
	if err != nil {
		return nil, err
	}
	if instance != "" {
		name, err := credentials.OutputValue(environment.ResourceOutputDir(s.env), instance, StorageBucketOutput)
		if err != nil {
			return nil, nserr.New(nserr.Usage, "%v. Add it with `nsctl bundle add storage --env %s` and `nsctl env apply %s`", err, s.Env, s.Env)
		}
		bucket = name
	}
	ok, err := s.bucketExists(ctx, bucket)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nserr.New(nserr.Usage, "environment %s has no bucket %s. `nsctl data storage --env %s` shows how to create one", s.Env, bucket, s.Env)
	}
	return &data.Storage{Env: s.Env, Target: to, Bucket: bucket, Prefix: prefix}, nil
}

// StorageInfo is what `nsctl data storage` prints.
type StorageInfo struct {
	Env              string   `json:"env"`
	Endpoint         string   `json:"endpoint"`
	InternalEndpoint string   `json:"internal_endpoint"`
	Network          string   `json:"network"`
	Region           string   `json:"region"`
	AccessKeyID      string   `json:"access_key_id"`
	SecretAccessKey  string   `json:"secret_access_key,omitempty"`
	Buckets          []string `json:"buckets"`
	AWSCLI           string   `json:"aws_cli"`
	Terraform        string   `json:"terraform"`
	Bundle           []string `json:"bundle"`
}

func storageInfo(s *envStorage, buckets []string, reveal bool) StorageInfo {
	secret := "<secret>"
	info := StorageInfo{Env: s.Env, Endpoint: s.Target.Endpoint, InternalEndpoint: floci.InternalEndpoint, Network: s.Network,
		Region: s.Target.Region, AccessKeyID: s.accessKey(), Buckets: buckets}
	if reveal {
		info.SecretAccessKey, secret = floci.SecretAccessKey, floci.SecretAccessKey
	}
	if info.Buckets == nil {
		info.Buckets = []string{}
	}
	info.AWSCLI = fmt.Sprintf("AWS_ACCESS_KEY_ID=%s AWS_SECRET_ACCESS_KEY=%s AWS_REGION=%s \\\n  aws --endpoint-url %s s3 mb s3://my-data",
		info.AccessKeyID, secret, info.Region, info.Endpoint)
	info.Terraform = fmt.Sprintf(`provider "aws" {
  region                      = %q
  access_key                  = %q
  secret_key                  = %q
  s3_use_path_style           = true
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
  endpoints {
    s3 = %q
  }
}

resource "aws_s3_bucket" "data" {
  bucket = "my-data"
}`, info.Region, info.AccessKeyID, secret, info.Endpoint)
	info.Bundle = []string{
		"nsctl bundle add storage --env " + s.Env,
		"nsctl env apply " + s.Env,
		"nsctl data land --env " + s.Env + " --to instance:storage",
	}
	return info
}

func newDataStorageCommand(d *dataCtx) *cobra.Command {
	var asJSON, reveal bool
	var env string
	cmd := &cobra.Command{
		Use:   "storage --env <name>",
		Short: "Show a local environment's S3, to land tables in",
		Long: `Storage is optional, and always the S3 service of a local NeuronSphere
environment. This prints that environment's S3 endpoint, from this machine and
from inside the environment, its region and access key, the buckets it has,
and ready-to-run AWS CLI and Terraform snippets for creating a bucket with
your own tools. The secret access key is withheld unless --reveal.

Or have a bucket set up for you: ` + "`nsctl bundle add storage --env <name>`" + ` and
` + "`nsctl env apply <name>`" + ` deploy one, which ` + "`nsctl data land --to instance:storage`" + `
then uses. nsctl data itself never deploys anything.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if env == "" {
				return nserr.New(nserr.Usage, "--env is required: the local environment whose storage to show (`nsctl env list`)")
			}
			s, err := d.storageOf(cmd.Context(), env)
			if err != nil {
				return err
			}
			buckets, err := s.buckets(cmd.Context())
			if err != nil {
				return err
			}
			info := storageInfo(s, buckets, reveal)
			if asJSON {
				return printJSON(cmd, info)
			}
			out := cmd.OutOrStdout()
			secret := "withheld; --reveal prints it"
			if reveal {
				secret = info.SecretAccessKey
			}
			fmt.Fprintf(out, "Storage in environment %s:\n\n", info.Env)
			fmt.Fprintf(out, "  From this machine:     %s\n", info.Endpoint)
			fmt.Fprintf(out, "  From the environment:  %s (network %s)\n", info.InternalEndpoint, info.Network)
			fmt.Fprintf(out, "  Region:                %s\n", info.Region)
			fmt.Fprintf(out, "  Access key id:         %s (it selects this environment's account)\n", info.AccessKeyID)
			fmt.Fprintf(out, "  Secret access key:     %s\n\n", secret)
			if len(info.Buckets) == 0 {
				fmt.Fprintln(out, "Buckets: none yet.")
			} else {
				fmt.Fprintf(out, "Buckets: %s\n", strings.Join(info.Buckets, ", "))
			}
			fmt.Fprintf(out, "\nCreate a bucket with any tool that speaks S3, for example the AWS CLI:\n\n%s\n", indentBy(info.AWSCLI, "  "))
			fmt.Fprintf(out, "\nor Terraform's AWS provider:\n\n%s\n", indentBy(info.Terraform, "  "))
			fmt.Fprintf(out, "\nthen land tables in it:\n\n  nsctl data land --env %s --to s3://my-data\n", info.Env)
			fmt.Fprintf(out, "\nOr have one set up for you:\n\n%s\n", indentBy(strings.Join(info.Bundle, "\n"), "  "))
			return nil
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "Local environment")
	cmd.Flags().BoolVar(&reveal, "reveal", false, "Print the secret access key")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	return cmd
}

func indentBy(s, pad string) string {
	return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
}

func newDataLandCommand(d *dataCtx) *cobra.Command {
	var asJSON, force bool
	var env, to string
	cmd := &cobra.Command{
		Use:   "land [<table>...] [--env <name> --to <target>]",
		Short: "Copy tables into a local environment's S3 and query them there",
		Long: `Writes each table (default: all of them) as Parquet under
<target>/raw/<table>/ in a local environment's S3, then points its view there.
The target is either a bucket that exists in that environment,
s3://<bucket>[/<prefix>], or instance:<name>, an hmd-inf-s3bucket instance
(such as the one ` + "`nsctl bundle add storage`" + ` adds), whose bucket is read from
what it produced.

--env and --to are needed the first time; the workspace remembers them. A
table whose files have not changed since it was landed is not written again
(--force writes it anyway).
From then on the engine joins the environment's network, so the environment
must be running to query the workspace. Your files are still only read.`,
		Example: `  nsctl data land --env local --to s3://my-data
  nsctl data land --env local --to instance:storage
  nsctl data land orders`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := d.open()
			if err != nil {
				return err
			}
			st := ws.Config.Storage
			if env == "" && st != nil {
				env = st.Env
			}
			if to == "" && st != nil && env == st.Env {
				to = st.Target
			}
			if env == "" || to == "" {
				return nserr.New(nserr.Usage, "workspace %s has no storage yet: pass --env <name> and --to s3://<bucket> or instance:<name>. `nsctl data storage --env <name>` shows what is there", ws.Name)
			}
			s, err := d.storageOf(cmd.Context(), env)
			if err != nil {
				return err
			}
			target, err := s.resolveTarget(cmd.Context(), to)
			if err != nil {
				return err
			}
			ws.Config.Storage, ws.Remote = target, s.remote()
			eng, err := d.engine(cmd.Context(), ws)
			if err != nil {
				return err
			}
			tables := args
			if len(tables) == 0 {
				all, err := ws.Tables()
				if err != nil {
					return err
				}
				for _, t := range all {
					tables = append(tables, t.Table)
				}
			}
			var reports []*data.LandReport
			for _, name := range tables {
				r, err := ws.Land(cmd.Context(), eng, name, force)
				if err != nil {
					return err
				}
				reports = append(reports, r)
			}
			if err := ws.Save(); err != nil {
				return err
			}
			reg, err := ws.Register(cmd.Context(), eng, false)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd, map[string]any{"storage": target, "landed": reports, "register": reg})
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Landed in environment %s, bucket %s:\n", env, target.Bucket)
			for _, r := range reports {
				note := ""
				if r.Unchanged {
					note = " (unchanged)"
				}
				fmt.Fprintf(out, "  %-20s %8d rows  %s%s\n", r.Table, r.Rows, r.URL, note)
			}
			fmt.Fprintln(out, "\nThe views now read the bucket; the environment must be running to query them.")
			return nil
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "Local environment (remembered)")
	cmd.Flags().StringVar(&to, "to", "", "s3://<bucket>[/<prefix>] or instance:<name> (remembered)")
	cmd.Flags().BoolVar(&force, "force", false, "Write every table again, even if its files have not changed")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the result as JSON")
	return cmd
}
