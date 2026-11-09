package engine

import (
	"context"
	"errors"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/starriv/cloudshuttle/internal/domain"
)

// s3ObjectGuard pins a source to its ETag for DeleteObject. rclone's
// generic Remove has no precondition argument.
type s3ObjectGuard struct {
	client *s3.Client
	bucket string
	key    string
	etag   string
}

func guardS3Object(ctx context.Context, loc Location, obj fs.Object) (*s3ObjectGuard, error) {
	backend, err := fs.Find("s3")
	if err != nil {
		return nil, err
	}
	enc := backend.Options.Get("encoding").Default.(encoder.MultiEncoder)
	region := loc.S3.Region
	if region == "" {
		region = "us-east-1"
	}
	client := s3.NewFromConfig(aws.Config{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(loc.S3.AccessKeyID, loc.S3.SecretAccessKey, ""),
		HTTPClient:  fshttp.NewClient(ctx),
	}, func(o *s3.Options) {
		if loc.S3.Endpoint != "" {
			o.BaseEndpoint = aws.String(loc.S3.Endpoint)
		}
		o.UsePathStyle = true
	})
	guard := &s3ObjectGuard{
		client: client, bucket: enc.FromStandardName(loc.S3.Bucket), key: enc.FromStandardPath(path.Join(loc.Path, obj.Remote())),
	}
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &guard.bucket, Key: &guard.key,
	})
	if err != nil {
		return nil, err
	}
	if head.ETag == nil || *head.ETag == "" || head.ContentLength == nil || head.LastModified == nil {
		return nil, errors.New("head object did not return its identity")
	}
	if *head.ContentLength != obj.Size() || !head.LastModified.Equal(obj.ModTime(ctx)) {
		return nil, domain.ErrStorageEntryChanged
	}
	guard.etag = strings.TrimSpace(*head.ETag)
	return guard, nil
}

func (g *s3ObjectGuard) remove(ctx context.Context) error {
	_, err := g.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &g.bucket, Key: &g.key, IfMatch: &g.etag,
	})
	var status interface{ HTTPStatusCode() int }
	if errors.As(err, &status) && (status.HTTPStatusCode() == 412 || status.HTTPStatusCode() == 404) {
		return domain.ErrStorageEntryChanged
	}
	return err
}
