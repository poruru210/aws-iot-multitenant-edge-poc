package device

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type fakeS3 struct {
	mu                          sync.Mutex
	parts                       map[int32]types.Part
	fail                        int32
	creates, uploads, completes int
}

func (s *fakeS3) CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	s.creates++
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String("upload1")}, nil
}
func (s *fakeS3) ListParts(context.Context, *s3.ListPartsInput, ...func(*s3.Options)) (*s3.ListPartsOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &s3.ListPartsOutput{}
	for _, p := range s.parts {
		out.Parts = append(out.Parts, p)
	}
	return out, nil
}
func (s *fakeS3) UploadPart(_ context.Context, in *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := aws.ToInt32(in.PartNumber)
	if n == s.fail {
		return nil, errors.New("simulated disconnect")
	}
	data, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	checksum := base64.StdEncoding.EncodeToString(sum[:])
	if checksum != aws.ToString(in.ChecksumSHA256) {
		return nil, errors.New("checksum mismatch")
	}
	s.uploads++
	s.parts[n] = types.Part{PartNumber: aws.Int32(n), Size: aws.Int64(int64(len(data))), ETag: aws.String("etag"), ChecksumSHA256: aws.String(checksum)}
	return &s3.UploadPartOutput{ETag: aws.String("etag"), ChecksumSHA256: aws.String(checksum)}, nil
}
func (s *fakeS3) CompleteMultipartUpload(_ context.Context, in *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	s.completes++
	for i, p := range in.MultipartUpload.Parts {
		if aws.ToInt32(p.PartNumber) != int32(i+1) {
			return nil, errors.New("unsorted parts")
		}
	}
	return &s3.CompleteMultipartUploadOutput{ETag: aws.String("complete-etag")}, nil
}
func (s *fakeS3) AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	return &s3.AbortMultipartUploadOutput{}, nil
}
func TestMultipartResumeAndReceipt(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "data.bin")
	state := filepath.Join(dir, "upload.json")
	if err := os.WriteFile(file, make([]byte, 5*1024*1024+100), 0600); err != nil {
		t.Fatal(err)
	}
	c := Config{TenantID: "tenant-a", ThingName: "edge-001", Bucket: "test-bucket"}
	api := &fakeS3{parts: make(map[int32]types.Part), fail: 2}
	_, err := Upload(context.Background(), api, c, file, "data.bin", state, 5*1024*1024, 1)
	if err == nil || api.uploads != 1 {
		t.Fatal("expected one part and a failure", err)
	}
	api.fail = 0
	etag, err := Upload(context.Background(), api, c, file, "data.bin", state, 5*1024*1024, 2)
	if err != nil || etag != "complete-etag" || api.creates != 1 || api.uploads != 2 || api.completes != 1 {
		t.Fatal(etag, err, api.creates, api.uploads, api.completes)
	}
	if _, err = Upload(context.Background(), api, c, file, "data.bin", state, 5*1024*1024, 2); err != nil || api.completes != 1 {
		t.Fatal("receipt not idempotent", err)
	}
	if err = os.WriteFile(file, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Upload(context.Background(), api, c, file, "data.bin", state, 5*1024*1024, 1); err == nil {
		t.Fatal("accepted changed source")
	}
}
func TestUploadRejectsUnsafeKeyBeforeAWS(t *testing.T) {
	if _, err := Upload(context.Background(), nil, Config{TenantID: "a", ThingName: "b"}, "", "../other", "", 5*1024*1024, 1); err == nil {
		t.Fatal("accepted traversal")
	}
}
