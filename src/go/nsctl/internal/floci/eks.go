package floci

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
)

// eksAPI is the slice of EKS the k3s reconcile uses.
type eksAPI interface {
	DescribeCluster(ctx context.Context, in *eks.DescribeClusterInput, opts ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	DeleteCluster(ctx context.Context, in *eks.DeleteClusterInput, opts ...func(*eks.Options)) (*eks.DeleteClusterOutput, error)
}

// Clusters answers the two EKS questions the k3s reconcile asks.
//
// These are nsctl's first EKS calls. They exist because Terraform reconciles
// the cluster *record*, not the container behind it: a record Floci still
// reports as ACTIVE is "no changes" to a re-apply, however dead the container
// is. Clearing the record is what makes the next deploy rebuild the cluster.
type Clusters struct{ EKS eksAPI }

// NewClusters builds a Clusters from a target's AWS config.
func NewClusters(ctx context.Context, t Target) (*Clusters, error) {
	cfg, err := t.Config(ctx)
	if err != nil {
		return nil, err
	}
	return &Clusters{EKS: eks.NewFromConfig(cfg)}, nil
}

// ClusterExists reports whether Floci holds a cluster record of that name, and
// whether it could be asked at all.
//
// The second return is not decoration. A cluster is never destroyed over an
// unanswered question: "Floci did not reply" and "there is no such cluster"
// lead to opposite actions, and conflating them deletes live clusters.
func (c *Clusters) ClusterExists(ctx context.Context, name string) (exists, ok bool) {
	if name == "" {
		return false, false
	}
	_, err := c.EKS.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(name)})
	if err == nil {
		return true, true
	}
	if isAPIErrorCode(err, "ResourceNotFoundException", "NotFoundException") {
		return false, true
	}
	return false, false
}

// DeleteCluster removes the record, tolerating one that is already gone.
func (c *Clusters) DeleteCluster(ctx context.Context, name string) error {
	_, err := c.EKS.DeleteCluster(ctx, &eks.DeleteClusterInput{Name: aws.String(name)})
	if err != nil && !isAPIErrorCode(err, "ResourceNotFoundException", "NotFoundException") {
		return fmt.Errorf("deleting the cluster %s: %w", name, err)
	}
	return nil
}
