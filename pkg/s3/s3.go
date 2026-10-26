package s3

import (
	"fmt"
	"time"

	"github.com/hieuvu0211/full-service-shopping/be/pkg/config"
)

// Client cấu hình kết nối thẳng AWS S3 (không dùng MinIO)
type Client struct {
	Region string
	Bucket string
}

func NewFromEnv() *Client {
	return &Client{
		Region: config.EnvOrDefault("AWS_REGION", "ap-southeast-1"),
		Bucket: config.EnvOrDefault("AWS_S3_BUCKET", "commerce-lab-retail-assets"),
	}
}

func (c *Client) ObjectURL(objectKey string) string {
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", c.Bucket, c.Region, objectKey)
}

func (c *Client) PresignUploadURL(objectKey string, ttl time.Duration) string {
	exp := time.Now().UTC().Add(ttl).Unix()
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s?X-Amz-Expires=%d", c.Bucket, c.Region, objectKey, exp)
}
