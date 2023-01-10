package mc

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/minio/madmin-go"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/sirupsen/logrus"
)

const policy = `{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": [
                "s3:ListAllMyBuckets"
            ],
            "Resource": [
                "arn:aws:s3:::%[1]s"
            ]
        },
        {
            "Effect": "Allow",
            "Action": [
                "s3:GetBucketLocation",
                "s3:ListBucket"
            ],
            "Resource": [
                "arn:aws:s3:::%[1]s"
            ]
        },
        {
            "Effect": "Allow",
            "Action": [
                "s3:*"
            ],
            "Resource": [
                "arn:aws:s3:::%[1]s/*"
            ]
        }
    ]
}`

func Policy(bucket string) []byte {
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(fmt.Sprintf(policy, bucket))); err != nil {
		panic(err)
	}
	return b.Bytes()
}

type Client struct {
	*madmin.AdminClient
	*minio.Client
}

func New(endpoint, accessKey, secretKey string, secure bool) (*Client, error) {
	ac, err := madmin.New(endpoint, accessKey, secretKey, secure)
	if err != nil {
		return nil, fmt.Errorf("failed to create minio admin client: %w", err)
	}
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create minio client: %w", err)
	}
	return &Client{AdminClient: ac, Client: c}, nil
}

func (c *Client) URL() *url.URL {
	return c.Client.EndpointURL()
}

func (c *Client) Endpoint() string {
	return c.Client.EndpointURL().Host
}

func (c *Client) Secure() bool {
	return c.Client.EndpointURL().Scheme == "https"
}

func GeneratePassword() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		logrus.Fatalf("failed to generate password: %v", err)
	}
	return base64.RawStdEncoding.EncodeToString(b)
}
