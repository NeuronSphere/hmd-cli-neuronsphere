package cmd

import (
	"strings"
	"testing"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
)

// Requirements: HMD_CLI_NEURONSPHERE_NERD039_SPEC009
func TestDataStorageWithholdsTheSecretAndOffersBothRoutes(t *testing.T) {
	t.Parallel()
	s := &envStorage{Env: "dev", Network: "net", Target: floci.Target{Endpoint: "http://localhost:4566",
		AccountID: "000000000010", AccessKeyID: "000000000010", Region: "us-west-2"}}
	info := storageInfo(s, nil, false)
	all := info.AWSCLI + info.Terraform + strings.Join(info.Bundle, "\n")
	if info.SecretAccessKey != "" || strings.Contains(all, floci.SecretAccessKey) {
		t.Errorf("the secret must be withheld without --reveal: %+v", info)
	}
	for _, want := range []string{"aws --endpoint-url http://localhost:4566 s3 mb", "s3_use_path_style           = true",
		"nsctl bundle add storage --env dev", "--to instance:storage"} {
		if !strings.Contains(all, want) {
			t.Errorf("storage output lacks %q", want)
		}
	}
	if info.Buckets == nil || info.InternalEndpoint != floci.InternalEndpoint {
		t.Errorf("info = %+v", info)
	}
	if r := storageInfo(s, []string{"b"}, true); r.SecretAccessKey != floci.SecretAccessKey || !strings.Contains(r.AWSCLI, floci.SecretAccessKey) {
		t.Errorf("--reveal prints it: %+v", r)
	}
	if r := s.remote(); r.Endpoint != "neuronsphere:4566" || r.KeyID != "000000000010" || r.Network != "net" {
		t.Errorf("remote = %+v", r)
	}
}
