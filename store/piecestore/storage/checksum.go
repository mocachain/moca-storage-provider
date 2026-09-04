package storage

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"reflect"
	"strconv"

	"github.com/mocachain/moca-storage-provider/pkg/log"
)

var crc32c = crc32.MakeTable(crc32.Castagnoli)

func generateChecksum(rs io.ReadSeeker) string {
	if b, ok := rs.(*bytes.Reader); ok {
		v := reflect.ValueOf(b)
		data := v.Elem().Field(0).Bytes()
		return strconv.Itoa(int(crc32.Update(0, crc32c, data)))
	}

	var hash uint32
	crcBuffer := bufPool.Get().(*[]byte)
	defer bufPool.Put(crcBuffer)
	defer func() { _, _ = rs.Seek(0, io.SeekStart) }()
	for {
		n, err := rs.Read(*crcBuffer)
		hash = crc32.Update(hash, crc32c, (*crcBuffer)[:n])
		if err != nil {
			if err != io.EOF {
				return ""
			}
			break
		}
	}
	return strconv.Itoa(int(hash))
}

type checksumReader struct {
	io.ReadCloser
	expected uint32
	checksum uint32
}

func (c *checksumReader) Read(buf []byte) (n int, err error) {
	n, err = c.ReadCloser.Read(buf)
	c.checksum = crc32.Update(c.checksum, crc32c, buf[:n])
	if err == io.EOF && c.checksum != c.expected {
		return 0, fmt.Errorf("failed to verify checksum: %d != %d", c.checksum, c.expected)
	}
	return
}

// verifiedRange reads a whole object through its verifying reader and serves
// the requested slice, so a ranged read is integrity-checked before any byte
// is returned; an empty checksum degrades to an unverified local slice.
func verifiedRange(rc io.ReadCloser, checksum string, offset, limit int64) (io.ReadCloser, error) {
	vr := verifyChecksum(rc, checksum)
	defer vr.Close()
	data, err := io.ReadAll(vr)
	if err != nil {
		return nil, err
	}
	if offset < 0 || offset >= int64(len(data)) {
		return nil, fmt.Errorf("invalid range: offset %d outside object of size %d", offset, len(data))
	}
	end := int64(len(data))
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return io.NopCloser(bytes.NewReader(data[offset:end])), nil
}

func verifyChecksum(rc io.ReadCloser, checksum string) io.ReadCloser {
	if checksum == "" {
		return rc
	}
	expected, err := strconv.ParseUint(checksum, 10, 32)
	if err != nil {
		signedExpected, signedErr := strconv.ParseInt(checksum, 10, 32)
		if signedErr != nil {
			log.Errorf("invalid crc32c: %s", checksum)
			return rc
		}
		expected = uint64(uint32(signedExpected))
	}
	return &checksumReader{rc, uint32(expected), 0}
}
