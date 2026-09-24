package floci

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// The two stores a local secret can live in.
//
// They are separate namespaces in the emulator and a reader must match its
// writer: hmd_lib_secrets_backend.create_secret() writes Parameter Store
// whatever its name suggests, while a chart's ExternalSecret may resolve
// through the Secrets Manager ClusterSecretStore. Looking in the wrong one
// reports "does not exist" against a secret that is present in the other.
const (
	SecretsManagerStore = "secrets-manager"
	ParameterStoreStore = "parameter-store"
)

// ErrNoSuchSecret is a name neither store holds. Distinguished from a transport
// failure because they call for different words: one says the deploy has not
// written it yet, the other that Floci is not answering.
var ErrNoSuchSecret = errors.New("no such secret")

type secretsGetAPI interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

type ssmGetAPI interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, opts ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// SecretReader reads one account's secrets out of Floci.
//
// Which account is decided by the credential, not by the endpoint: one Floci
// serves every account and resolves the owner from the 12-digit access key id,
// so a reader built from an environment's Target reads that environment's
// secrets and no other's.
type SecretReader struct {
	Secrets secretsGetAPI
	Params  ssmGetAPI
}

// NewSecretReader builds a reader from a target's AWS config.
func NewSecretReader(ctx context.Context, t Target) (*SecretReader, error) {
	cfg, err := t.Config(ctx)
	if err != nil {
		return nil, err
	}
	return &SecretReader{
		Secrets: secretsmanager.NewFromConfig(cfg),
		Params:  ssm.NewFromConfig(cfg),
	}, nil
}

// Get returns a secret's raw value from the named store.
func (r *SecretReader) Get(ctx context.Context, store, name string) (string, error) {
	switch store {
	case SecretsManagerStore:
		out, err := r.Secrets.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(name)})
		if err != nil {
			if isAPIErrorCode(err, "ResourceNotFoundException") {
				return "", fmt.Errorf("%w: %s in Secrets Manager", ErrNoSuchSecret, name)
			}
			return "", fmt.Errorf("reading the secret %s: %w", name, err)
		}
		if out.SecretString == nil {
			return "", fmt.Errorf("%w: %s holds no string value", ErrNoSuchSecret, name)
		}
		return *out.SecretString, nil
	case ParameterStoreStore:
		out, err := r.Params.GetParameter(ctx, &ssm.GetParameterInput{
			Name: aws.String(name), WithDecryption: aws.Bool(true),
		})
		if err != nil {
			if isAPIErrorCode(err, "ParameterNotFound") {
				return "", fmt.Errorf("%w: %s in Parameter Store", ErrNoSuchSecret, name)
			}
			return "", fmt.Errorf("reading the parameter %s: %w", name, err)
		}
		if out.Parameter == nil || out.Parameter.Value == nil {
			return "", fmt.Errorf("%w: %s holds no value", ErrNoSuchSecret, name)
		}
		return *out.Parameter.Value, nil
	default:
		return "", fmt.Errorf("unknown secret store %q; expected %s or %s",
			store, SecretsManagerStore, ParameterStoreStore)
	}
}
