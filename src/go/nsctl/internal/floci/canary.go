package floci

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// storageCanaryKey is where the round trip below writes.
//
// Under the same `hmd/` prefix hmd-lib-cdktf's S3Backend puts its state, so the
// probe exercises the path a deploy will actually take rather than a
// neighbouring one. The leading dot keeps it out of the way of `tofu`'s own
// listing of workspaces.
const storageCanaryKey = "hmd/.nsctl-storage-check"

// CheckStorage proves the object store by round trip: write, read back, compare
// the bytes, delete.
//
// A Floci that answers is not a Floci that works. Its state is held in memory
// and flushed to the directory bound at /app/data, so when that directory stops
// being reachable -- the whole of NERD001 SPEC014 -- the control-plane calls
// still succeed from memory while every disk access fails. CreateBucket is the
// worst possible probe for this, because it is idempotent: it reports the
// bucket Floci loaded at startup and says nothing about whether anything can be
// read out of it.
//
// So nothing short of touching the disk distinguishes the two, and the cost of
// not distinguishing them is a deploy that dies roughly 950 log lines later in
// `tofu init`, with an S3 InternalError 500 that names no cause.
//
// The bytes are compared rather than the status code: a store that returns 200
// and the wrong object is a worse failure than one that errors, and it is the
// shape a half-written directory takes.
func (p *Provisioner) CheckStorage(ctx context.Context, bucket string) error {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return fmt.Errorf("generating the storage probe's token: %w", err)
	}
	want := []byte(hex.EncodeToString(token))

	if _, err := p.S3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(storageCanaryKey), Body: bytes.NewReader(want),
	}); err != nil {
		return p.storageError(bucket, "writing to", err)
	}
	// Deleted even when the read fails: the probe must not leave the object
	// behind on the one path where someone is about to go looking at the
	// bucket by hand.
	defer func() {
		_, _ = p.S3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(storageCanaryKey),
		})
	}()

	out, err := p.S3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(storageCanaryKey),
	})
	if err != nil {
		return p.storageError(bucket, "reading back from", err)
	}
	defer out.Body.Close()

	got, err := io.ReadAll(out.Body)
	if err != nil {
		return p.storageError(bucket, "reading back from", err)
	}
	if !bytes.Equal(got, want) {
		return p.storageError(bucket, "reading back from",
			fmt.Errorf("the object came back with %d byte(s) that are not what was written", len(got)))
	}
	return nil
}

// storageError says what the reader has to know to act: which bucket, which
// endpoint, and that the directory behind it is the thing to look at.
func (p *Provisioner) storageError(bucket, verb string, err error) error {
	return fmt.Errorf(
		"Floci is reachable but its object storage is not working: %s the bucket %s at %s failed:\n  %v\n"+
			"  Floci keeps that bucket in $HMD_HOME/floci/data, bound into the container when it was created. "+
			"If $HMD_HOME was deleted or moved while Floci was running, the container is still writing to the old directory and cannot read anything back.\n"+
			"  %s",
		verb, bucket, p.Target.Endpoint, err, DoNotDeleteHome)
}
