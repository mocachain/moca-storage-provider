package storage

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_generateChecksum(t *testing.T) {
	r := bytes.NewReader([]byte("test"))
	assert.NotNil(t, r)
	cases := []struct {
		name         string
		rs           io.ReadSeeker
		wantedResult string
	}{
		{
			name:         "bytes Reader",
			rs:           r,
			wantedResult: "2258662080",
		},
		{
			name:         "strings Reader",
			rs:           strings.NewReader("hello world"),
			wantedResult: "3381945770",
		},
		{
			name:         "returned checksum is empty",
			rs:           strings.NewReader(""),
			wantedResult: "0",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result := generateChecksum(tt.rs)
			assert.Equal(t, tt.wantedResult, result)
		})
	}
}

func Test_checksumReaderRead(t *testing.T) {
	cases := []struct {
		name      string
		cr        *checksumReader
		buf       []byte
		wantedN   int
		wantedErr error
	}{
		{
			name: "1",
			cr: &checksumReader{
				ReadCloser: io.NopCloser(strings.NewReader("hello world")),
			},
			buf:       []byte("test"),
			wantedN:   4,
			wantedErr: nil,
		},
		{
			name: "2",
			cr: &checksumReader{
				ReadCloser: io.NopCloser(strings.NewReader("")),
				expected:   5,
			},
			buf:       []byte("test"),
			wantedN:   0,
			wantedErr: errors.New("failed to verify checksum: 0 != 5"),
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.cr.Read(tt.buf)
			assert.Equal(t, tt.wantedN, result)
			assert.Equal(t, tt.wantedErr, err)
		})
	}
}

func Test_verifyChecksum(t *testing.T) {
	cases := []struct {
		name         string
		rc           io.ReadCloser
		checksum     string
		wantedResult io.ReadCloser
	}{
		{
			name:         "1",
			rc:           io.NopCloser(strings.NewReader("hello world")),
			checksum:     "",
			wantedResult: io.NopCloser(strings.NewReader("hello world")),
		},
		{
			name:     "3",
			rc:       io.NopCloser(strings.NewReader("hello world")),
			checksum: "10",
			wantedResult: &checksumReader{
				ReadCloser: io.NopCloser(strings.NewReader("hello world")),
				expected:   10,
				checksum:   0,
			},
		},
		{
			name:     "accepts a valid unsigned crc32c value",
			rc:       io.NopCloser(strings.NewReader("hello world")),
			checksum: "3381945770",
			wantedResult: &checksumReader{
				ReadCloser: io.NopCloser(strings.NewReader("hello world")),
				expected:   3381945770,
				checksum:   0,
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result, err := verifyChecksum(tt.rc, tt.checksum)
			assert.NoError(t, err)
			assert.Equal(t, result, tt.wantedResult)
		})
	}
}

// closeRecorder records whether the wrapped body was closed.
type closeRecorder struct {
	io.Reader
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

var invalidChecksums = []struct {
	name     string
	checksum string
}{
	{name: "non-numeric", checksum: "abc"},
	{name: "above uint32 range", checksum: "4294967296"},
	{name: "below int32 range", checksum: "-2147483649"},
	{name: "leading space", checksum: " 1"},
}

func Test_verifyChecksumRejectsInvalidChecksum(t *testing.T) {
	for _, tt := range invalidChecksums {
		t.Run(tt.name, func(t *testing.T) {
			rc := &closeRecorder{Reader: strings.NewReader("hello world")}
			result, err := verifyChecksum(rc, tt.checksum)
			assert.ErrorIs(t, err, ErrInvalidChecksum)
			assert.Nil(t, result)
			assert.True(t, rc.closed)
		})
	}
}

func Test_verifiedRangeRejectsInvalidChecksum(t *testing.T) {
	for _, tt := range invalidChecksums {
		t.Run(tt.name, func(t *testing.T) {
			rc := &closeRecorder{Reader: strings.NewReader("hello world")}
			result, err := verifiedRange(rc, tt.checksum, 3, 3)
			assert.ErrorIs(t, err, ErrInvalidChecksum)
			assert.Nil(t, result)
			assert.True(t, rc.closed)
		})
	}
}

func Test_verifyChecksumReadsValidChecksums(t *testing.T) {
	cases := []struct {
		name     string
		checksum string
	}{
		{
			name:     "unsigned crc32c with high bit set",
			checksum: "3381945770",
		},
		{
			name:     "legacy signed crc32c with high bit set",
			checksum: "-913021526",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result, err := verifyChecksum(io.NopCloser(strings.NewReader("hello world")), tt.checksum)
			assert.NoError(t, err)
			assert.IsType(t, &checksumReader{}, result)

			_, err = io.ReadAll(result)
			assert.NoError(t, err)
		})
	}
}
