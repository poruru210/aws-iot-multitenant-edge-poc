package device

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/poruru210/aws-iot-multitenant-edge-poc/internal/identity"
)

type S3API interface {
	CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	UploadPart(context.Context, *s3.UploadPartInput, ...func(*s3.Options)) (*s3.UploadPartOutput, error)
	ListParts(context.Context, *s3.ListPartsInput, ...func(*s3.Options)) (*s3.ListPartsOutput, error)
	CompleteMultipartUpload(context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
}
type UploadState struct {
	Version  int    `json:"version"`
	Bucket   string `json:"bucket"`
	Key      string `json:"key"`
	UploadID string `json:"uploadId"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	PartSize int64  `json:"partSize"`
	Phase    string `json:"phase"`
	ETag     string `json:"etag,omitempty"`
}

func digest(f *os.File, offset, size int64) ([]byte, error) {
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, offset, size)); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
func uploadLock(stateFile string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(stateFile), 0700); err != nil {
		return nil, err
	}
	lock := stateFile + ".lock"
	if err := identity.NewPrivate(lock, []byte(fmt.Sprintf("pid=%d\n", os.Getpid()))); err != nil {
		return nil, fmt.Errorf("upload locked: %w; remove stale .lock only after verifying its process stopped", err)
	}
	return func() { _ = os.Remove(lock) }, nil
}

// Upload resumes from S3 ListParts; the checkpoint contains only the upload
// identity, never AWS credentials. Files must be immutable during transfer.
func Upload(ctx context.Context, api S3API, c Config, source, relative, stateFile string, partSize int64, concurrency int) (string, error) {
	key, err := identity.ObjectKey(c.TenantID, c.ThingName, relative)
	if err != nil {
		return "", err
	}
	if partSize < 5*1024*1024 || partSize > 512*1024*1024 || concurrency < 1 || concurrency > 16 {
		return "", errors.New("part size must be 5-512 MiB and concurrency 1-16")
	}
	unlock, err := uploadLock(stateFile)
	if err != nil {
		return "", err
	}
	defer unlock()
	f, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return "", errors.New("source must be a nonempty regular file")
	}
	count := (info.Size() + partSize - 1) / partSize
	if count > 10000 {
		return "", errors.New("more than 10000 parts: increase part size")
	}
	sum, err := digest(f, 0, info.Size())
	if err != nil {
		return "", err
	}
	fileHash := hex.EncodeToString(sum)
	var state UploadState
	if err = identity.ReadJSON(stateFile, &state); err == nil {
		if state.Version != 1 || state.Bucket != c.Bucket || state.Key != key || state.Size != info.Size() || state.SHA256 != fileHash || state.PartSize != partSize || state.UploadID == "" {
			return "", errors.New("checkpoint does not match file, destination or part size")
		}
		if state.Phase == "COMPLETE" {
			return state.ETag, nil
		}
		if state.Phase != "UPLOADING" && state.Phase != "COMPLETING" {
			return "", errors.New("invalid checkpoint phase")
		}
	} else {
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		created, e := api.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: &c.Bucket, Key: &key, ChecksumAlgorithm: types.ChecksumAlgorithmSha256, Metadata: map[string]string{"source-sha256": fileHash}})
		if e != nil {
			return "", e
		}
		if created.UploadId == nil || *created.UploadId == "" {
			return "", errors.New("S3 returned no upload ID")
		}
		state = UploadState{Version: 1, Bucket: c.Bucket, Key: key, UploadID: *created.UploadId, SHA256: fileHash, Size: info.Size(), PartSize: partSize, Phase: "UPLOADING"}
		if err = identity.AtomicPrivate(stateFile, state); err != nil {
			// A failed local checkpoint must not leave an untracked upload indefinitely.
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			_, _ = api.AbortMultipartUpload(cleanup, &s3.AbortMultipartUploadInput{Bucket: &c.Bucket, Key: &key, UploadId: &state.UploadID})
			return "", err
		}
	}
	checksums := make(map[int32]string)
	sizes := make(map[int32]int64)
	for n := int32(1); int64(n) <= count; n++ {
		offset := int64(n-1) * partSize
		size := min(partSize, info.Size()-offset)
		partHash, e := digest(f, offset, size)
		if e != nil {
			return "", e
		}
		checksums[n], sizes[n] = base64.StdEncoding.EncodeToString(partHash), size
	}
	completed := make(map[int32]types.CompletedPart)
	pages := s3.NewListPartsPaginator(api, &s3.ListPartsInput{Bucket: &c.Bucket, Key: &key, UploadId: &state.UploadID})
	for pages.HasMorePages() {
		page, e := pages.NextPage(ctx)
		if e != nil {
			return "", fmt.Errorf("cannot reconcile multipart state (after COMPLETING, success may be ambiguous; verify with an operator): %w", e)
		}
		for _, p := range page.Parts {
			n := aws.ToInt32(p.PartNumber)
			if n < 1 || int64(n) > count || aws.ToInt64(p.Size) != sizes[n] || aws.ToString(p.ChecksumSHA256) != checksums[n] || aws.ToString(p.ETag) == "" {
				return "", errors.New("remote part does not match local file")
			}
			completed[n] = types.CompletedPart{PartNumber: p.PartNumber, ETag: p.ETag, ChecksumSHA256: p.ChecksumSHA256}
		}
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int32, int(count))
	for n := int32(1); int64(n) <= count; n++ {
		if _, ok := completed[n]; !ok {
			jobs <- n
		}
	}
	close(jobs)
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := make(chan error, 1)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				if workCtx.Err() != nil {
					return
				}
				out, e := api.UploadPart(workCtx, &s3.UploadPartInput{Bucket: &c.Bucket, Key: &key, UploadId: &state.UploadID, PartNumber: aws.Int32(n), Body: io.NewSectionReader(f, int64(n-1)*partSize, sizes[n]), ContentLength: aws.Int64(sizes[n]), ChecksumAlgorithm: types.ChecksumAlgorithmSha256, ChecksumSHA256: aws.String(checksums[n])})
				if e == nil && (aws.ToString(out.ETag) == "" || aws.ToString(out.ChecksumSHA256) != checksums[n]) {
					e = errors.New("S3 part checksum mismatch")
				}
				if e != nil {
					select {
					case failures <- e:
					default:
					}
					cancel()
					return
				}
				mu.Lock()
				completed[n] = types.CompletedPart{PartNumber: aws.Int32(n), ETag: out.ETag, ChecksumSHA256: out.ChecksumSHA256}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	select {
	case e := <-failures:
		return "", e
	default:
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if int64(len(completed)) != count {
		return "", errors.New("not all parts uploaded")
	}
	finalHash, err := digest(f, 0, info.Size())
	if err != nil {
		return "", err
	}
	latest, err := f.Stat()
	if err != nil {
		return "", err
	}
	if hex.EncodeToString(finalHash) != fileHash || latest.Size() != info.Size() {
		return "", errors.New("source file changed; refusing completion")
	}
	parts := make([]types.CompletedPart, 0, len(completed))
	for _, p := range completed {
		parts = append(parts, p)
	}
	sort.Slice(parts, func(i, j int) bool { return aws.ToInt32(parts[i].PartNumber) < aws.ToInt32(parts[j].PartNumber) })
	state.Phase = "COMPLETING"
	if err = identity.AtomicPrivate(stateFile, state); err != nil {
		return "", err
	}
	result, err := api.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: &c.Bucket, Key: &key, UploadId: &state.UploadID, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts}})
	if err != nil {
		return "", fmt.Errorf("completion result may be ambiguous; keep checkpoint and verify S3 before restarting: %w", err)
	}
	state.Phase, state.ETag = "COMPLETE", aws.ToString(result.ETag)
	if err = identity.AtomicPrivate(stateFile, state); err != nil {
		return "", fmt.Errorf("S3 completed but local receipt failed: %w", err)
	}
	return state.ETag, nil
}
