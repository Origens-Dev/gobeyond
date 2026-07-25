package s3

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/Origens-Dev/gobeyond/imageopt"
)

type fakeS3Client struct {
	input *awss3.GetObjectInput
	body  string
	err   error
}

func (client *fakeS3Client) GetObject(_ context.Context, input *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	client.input = input
	if client.err != nil {
		return nil, client.err
	}
	return &awss3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(client.body))}, nil
}

func TestLoaderMapsSiteScopedKey(t *testing.T) {
	client := &fakeS3Client{body: "image"}
	loader := Loader{Client: client, Bucket: "gobeyond-prod-site-static", Prefix: "landing"}

	body, err := loader.Open(context.Background(), "/brand/logo.png")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if client.input == nil {
		t.Fatal("GetObject was not called")
	}
	if got := aws.ToString(client.input.Bucket); got != "gobeyond-prod-site-static" {
		t.Fatalf("bucket = %q", got)
	}
	if got := aws.ToString(client.input.Key); got != "landing/brand/logo.png" {
		t.Fatalf("key = %q", got)
	}
}

func TestLoaderRejectsTraversalBeforeGetObject(t *testing.T) {
	client := &fakeS3Client{}
	loader := Loader{Client: client, Bucket: "bucket", Prefix: "app"}
	if _, err := loader.Open(context.Background(), "/%2e%2e/secret.png"); !errors.Is(err, imageopt.ErrInvalidSource) {
		t.Fatalf("error = %v, want ErrInvalidSource", err)
	}
	if client.input != nil {
		t.Fatal("GetObject called for invalid source")
	}
}

func TestLoaderMapsNoSuchKey(t *testing.T) {
	loader := Loader{
		Client: &fakeS3Client{err: &s3types.NoSuchKey{}},
		Bucket: "bucket",
		Prefix: "app",
	}
	if _, err := loader.Open(context.Background(), "/missing.png"); !errors.Is(err, imageopt.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestNewLoaderFromEnvironmentPrefersDisk(t *testing.T) {
	t.Setenv("GOBEYOND_STATIC_DIR", "")
	t.Setenv(imageopt.ImageSourceBucketEnv, "bucket")
	t.Setenv(imageopt.ImageSourcePrefixEnv, "landing")
	root := t.TempDir()

	loader, err := NewLoaderFromEnvironment(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	disk, ok := loader.(imageopt.DiskLoader)
	if !ok || disk.Root != filepath.Clean(root) {
		t.Fatalf("loader = %#v, want DiskLoader rooted at %q", loader, root)
	}
}

func TestNewLoaderFromEnvironmentWithoutConfiguration(t *testing.T) {
	t.Setenv("GOBEYOND_STATIC_DIR", "")
	t.Setenv(imageopt.ImageSourceBucketEnv, "")
	t.Setenv(imageopt.ImageSourcePrefixEnv, "")

	loader, err := NewLoaderFromEnvironment(context.Background(), "")
	if err != nil || loader != nil {
		t.Fatalf("loader = %#v, err = %v; want (nil, nil)", loader, err)
	}
}

func TestNewLoaderFromEnvironmentRequiresBothVariables(t *testing.T) {
	t.Setenv("GOBEYOND_STATIC_DIR", "")
	t.Setenv(imageopt.ImageSourceBucketEnv, "bucket")
	t.Setenv(imageopt.ImageSourcePrefixEnv, "")

	if _, err := NewLoaderFromEnvironment(context.Background(), ""); err == nil {
		t.Fatal("NewLoaderFromEnvironment succeeded with only a bucket configured")
	}
}
