package storage

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/stretchr/testify/assert"
)

type ossTestRoundTripper func(*http.Request) (*http.Response, error)

func (f ossTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestOSSStore_GetObjectRejectsRangeWithEmptyWholeObjectChecksum(t *testing.T) {
	body := &trackingReadCloser{Reader: strings.NewReader("oss get")}
	headers := make(http.Header)
	headers.Set(oss.HTTPHeaderOssMetaPrefix+ChecksumAlgo, "")
	client, err := oss.New("http://oss.example.com", "key", "secret",
		oss.UseCname(true), oss.HTTPClient(&http.Client{Transport: ossTestRoundTripper(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusPartialContent,
				Header:     headers,
				Body:       body,
				Request:    req,
			}, nil
		})}),
	)
	if !assert.NoError(t, err) {
		return
	}
	bucket, err := client.Bucket("bucket")
	if !assert.NoError(t, err) {
		return
	}

	data, err := (&ossStore{client: client, bucket: bucket}).GetObject(context.TODO(), mockKey, 0, 1)
	assert.ErrorIs(t, err, ErrRangeChecksumUnavailable)
	assert.Nil(t, data)
	assert.True(t, body.closed)
}
