// Package s3 signs and checks object-storage requests with the AWS SDK for Go v2. It talks to AWS S3 or to any
// S3-compatible server (MinIO and similar in development) when S3_ENDPOINT is set.
package s3

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
)

// Environment variables read by NewFromEnv.
const (
	EndpointEnvVar       = "S3_ENDPOINT"        // empty means AWS S3
	PublicEndpointEnvVar = "S3_PUBLIC_ENDPOINT" // endpoint browsers use in signed URLs; defaults to S3_ENDPOINT
	PathStyleEnvVar      = "S3_USE_PATH_STYLE"  // "true" for MinIO-like servers; implied by S3_ENDPOINT
	AccessKeyEnvVar      = "AWS_ACCESS_KEY_ID"
	SecretKeyEnvVar      = "AWS_SECRET_ACCESS_KEY"
	RegionEnvVar         = "AWS_REGION"
	BucketEnvVar         = "AWS_S3_BUCKET"
	defaultRegion        = "ap-southeast-1"
	defaultBucket        = "commerce-lab-retail-assets"
	maxPresignDuration   = 7 * 24 * time.Hour
)

// ErrObjectNotFound is returned by Head when the object does not exist.
var ErrObjectNotFound = errors.New("s3: object not found")

// Config configures a Client. Credentials are mandatory for signing; the client can still be created without
// them (a service that never signs, such as the ledger, keeps working) and signing then fails with an error.
type Config struct {
	Endpoint string
	// PublicEndpoint is the address clients outside the server network use. Signed URLs are built for it
	// (the host is part of the signature) while Head and EnsureBucket keep using Endpoint.
	PublicEndpoint string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	PathStyle      bool
}

// Client signs requests for one bucket.
type Client struct {
	Region string
	Bucket string

	endpoint string // reachable from this process
	public   string // used in URLs handed to clients
	api      *awss3.Client
	presign  *awss3.PresignClient
}

// New builds a client from cfg. It performs no network calls.
func New(cfg Config) *Client {
	if cfg.Region == "" {
		cfg.Region = defaultRegion
	}
	if cfg.Bucket == "" {
		cfg.Bucket = defaultBucket
	}
	build := func(endpoint string) *awss3.Client {
		opts := awss3.Options{
			Region:      cfg.Region,
			Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
			// Signed uploads must not require extra checksum headers that S3-compatible servers may not know.
			RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
			ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
			UsePathStyle:               cfg.PathStyle || endpoint != "",
		}
		if endpoint != "" {
			opts.BaseEndpoint = aws.String(endpoint)
		}
		return awss3.New(opts)
	}
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	public := strings.TrimRight(cfg.PublicEndpoint, "/")
	if public == "" {
		public = endpoint
	}
	api := build(endpoint)
	presignAPI := api
	if public != endpoint {
		presignAPI = build(public)
	}
	return &Client{
		Region: cfg.Region, Bucket: cfg.Bucket, endpoint: endpoint, public: public,
		api: api, presign: awss3.NewPresignClient(presignAPI),
	}
}

// NewFromEnv builds a client from the environment (see the *EnvVar constants).
func NewFromEnv() *Client {
	return New(Config{
		Endpoint:       config.EnvOrDefault(EndpointEnvVar, ""),
		PublicEndpoint: config.EnvOrDefault(PublicEndpointEnvVar, ""),
		Region:         config.EnvOrDefault(RegionEnvVar, defaultRegion),
		Bucket:         config.EnvOrDefault(BucketEnvVar, defaultBucket),
		AccessKey:      config.EnvOrDefault(AccessKeyEnvVar, ""),
		SecretKey:      config.EnvOrDefault(SecretKeyEnvVar, ""),
		PathStyle:      config.EnvOrDefault(PathStyleEnvVar, "") == "true",
	})
}

// ObjectURL is the plain (unsigned) URL of an object; it only works for publicly readable objects.
func (c *Client) ObjectURL(objectKey string) string {
	if c.public != "" {
		return fmt.Sprintf("%s/%s/%s", c.public, c.Bucket, objectKey)
	}
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", c.Bucket, c.Region, objectKey)
}

// PresignedRequest is a signed request the browser can send directly to the storage server.
type PresignedRequest struct {
	URL       string
	Method    string
	Headers   map[string]string // headers the sender must include exactly as given
	ExpiresAt time.Time
}

func checkTTL(ttl time.Duration) error {
	if ttl <= 0 || ttl > maxPresignDuration {
		return fmt.Errorf("s3: presign duration must be between 1ns and %s", maxPresignDuration)
	}
	return nil
}

// PresignPut signs an upload (HTTP PUT) of exactly contentLength bytes of contentType to key. The content type
// and length are part of the signature, so the storage server rejects an upload that differs from them.
func (c *Client) PresignPut(ctx context.Context, key, contentType string, contentLength int64, ttl time.Duration) (PresignedRequest, error) {
	if key == "" || contentType == "" || contentLength <= 0 {
		return PresignedRequest{}, errors.New("s3: key, content type and a positive content length are required")
	}
	if err := checkTTL(ttl); err != nil {
		return PresignedRequest{}, err
	}
	req, err := c.presign.PresignPutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(c.Bucket), Key: aws.String(key),
		ContentType: aws.String(contentType), ContentLength: aws.Int64(contentLength),
	}, awss3.WithPresignExpires(ttl))
	if err != nil {
		return PresignedRequest{}, fmt.Errorf("s3: presign put: %w", err)
	}
	return PresignedRequest{
		URL: req.URL, Method: req.Method, Headers: map[string]string{"Content-Type": contentType},
		ExpiresAt: time.Now().UTC().Add(ttl),
	}, nil
}

// PresignGet signs a download (HTTP GET) of key.
func (c *Client) PresignGet(ctx context.Context, key string, ttl time.Duration) (PresignedRequest, error) {
	if key == "" {
		return PresignedRequest{}, errors.New("s3: key is required")
	}
	if err := checkTTL(ttl); err != nil {
		return PresignedRequest{}, err
	}
	req, err := c.presign.PresignGetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(key)},
		awss3.WithPresignExpires(ttl))
	if err != nil {
		return PresignedRequest{}, fmt.Errorf("s3: presign get: %w", err)
	}
	return PresignedRequest{URL: req.URL, Method: http.MethodGet, ExpiresAt: time.Now().UTC().Add(ttl)}, nil
}

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Size        int64
	ContentType string
}

// Head returns the size and content type of key, or ErrObjectNotFound.
func (c *Client) Head(ctx context.Context, key string) (ObjectInfo, error) {
	out, err := c.api.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(key)})
	if err != nil {
		var notFound *s3types.NotFound
		var apiErr smithy.APIError
		if errors.As(err, &notFound) || (errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey")) {
			return ObjectInfo{}, ErrObjectNotFound
		}
		return ObjectInfo{}, fmt.Errorf("s3: head %s: %w", key, err)
	}
	return ObjectInfo{Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType)}, nil
}

// EnsureBucket creates the bucket when it does not exist yet. It is meant for development servers; production
// buckets are provisioned outside the application.
func (c *Client) EnsureBucket(ctx context.Context) error {
	_, err := c.api.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(c.Bucket)})
	if err == nil {
		return nil
	}
	if _, err := c.api.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(c.Bucket)}); err != nil {
		var owned *s3types.BucketAlreadyOwnedByYou
		if errors.As(err, &owned) {
			return nil
		}
		return fmt.Errorf("s3: create bucket %s: %w", c.Bucket, err)
	}
	return nil
}
