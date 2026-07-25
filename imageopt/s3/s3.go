// Package s3 provides the AWS-backed image source for GoBeyond's runtime
// image optimizer.
//
// It is a nested Go module so the AWS SDK stays out of every consumer's module
// graph: only apps that actually serve images from S3 require it. The AWS-free
// core (Loader, DiskLoader, Handler, optimize) stays in
// github.com/Origens-Dev/gobeyond/imageopt.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/Origens-Dev/gobeyond/imageopt"
)

// GetObjectAPI is the subset of the S3 client used by Loader.
type GetObjectAPI interface {
	GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
}

// Loader reads same-site static files from a site-scoped S3 prefix. It
// implements imageopt.Loader.
type Loader struct {
	Client GetObjectAPI
	Bucket string
	Prefix string
}

var _ imageopt.Loader = Loader{}

// NewLoaderFromEnvironment selects a disk source when diskRoot (or
// GOBEYOND_STATIC_DIR) is set, otherwise an S3 source when
// GOBEYOND_IMAGE_SOURCE_BUCKET and GOBEYOND_IMAGE_SOURCE_PREFIX are configured.
// It returns (nil, nil) when neither source is configured.
func NewLoaderFromEnvironment(ctx context.Context, diskRoot string) (imageopt.Loader, error) {
	if root, ok := imageopt.DiskRootFromEnvironment(diskRoot); ok {
		return imageopt.DiskLoader{Root: root}, nil
	}
	configured, err := imageopt.S3SourceFromEnvironment()
	if err != nil || !configured {
		return nil, err
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration for image source: %w", err)
	}
	return Loader{
		Client: awss3.NewFromConfig(cfg),
		Bucket: strings.TrimSpace(os.Getenv(imageopt.ImageSourceBucketEnv)),
		Prefix: strings.TrimSpace(os.Getenv(imageopt.ImageSourcePrefixEnv)),
	}, nil
}

// Open maps /path/to/image.png to <Prefix>/path/to/image.png.
func (loader Loader) Open(ctx context.Context, source string) (io.ReadCloser, error) {
	relative, err := imageopt.ValidateSource(source)
	if err != nil {
		return nil, err
	}
	prefix, err := imageopt.ValidatePrefix(loader.Prefix)
	if err != nil || loader.Client == nil || strings.TrimSpace(loader.Bucket) == "" {
		return nil, fmt.Errorf("configure S3 image source: %w", imageopt.ErrInvalidSource)
	}
	output, err := loader.Client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(loader.Bucket),
		Key:    aws.String(prefix + "/" + relative),
	})
	if err != nil {
		var noSuchKey *s3types.NoSuchKey
		var apiError smithy.APIError
		if errors.As(err, &noSuchKey) ||
			(errors.As(err, &apiError) && (apiError.ErrorCode() == "NoSuchKey" || apiError.ErrorCode() == "NotFound")) {
			return nil, imageopt.ErrNotFound
		}
		return nil, err
	}
	if output == nil || output.Body == nil {
		return nil, imageopt.ErrNotFound
	}
	return output.Body, nil
}
