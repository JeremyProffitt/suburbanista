package locationmap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/internal/auth"
	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

var objectID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var bucketID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

type S3Objects struct {
	Config aws.Config
	Bucket string
	HTTP   *http.Client
}

func (s S3Objects) request(ctx context.Context, method, id string, data []byte) (*http.Response, error) {
	if !bucketID.MatchString(s.Bucket) || !objectID.MatchString(id) || s.Config.Credentials == nil {
		return nil, errors.New("map-object-invalid")
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+s.Bucket+".s3."+s.Config.Region+".amazonaws.com/map-cache/"+id+".png", bytes.NewReader(data))
	if err != nil {
		return nil, auth.ErrUnavailable
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	req.Header.Set("x-amz-content-sha256", hash)
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "image/png")
		req.Header.Set("Cache-Control", "private,no-store")
		req.Header.Set("x-amz-server-side-encryption", "AES256")
		req.Header.Set("If-None-Match", "*")
	}
	credentials, err := s.Config.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, auth.ErrUnavailable
	}
	if v4.NewSigner().SignHTTP(ctx, credentials, req, hash, "s3", s.Config.Region, time.Now().UTC()) != nil {
		return nil, auth.ErrUnavailable
	}
	client := http.Client{Timeout: 10 * time.Second}
	if s.HTTP != nil {
		client = *s.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return nil, auth.ErrUnavailable
	}
	return res, nil
}
func (s S3Objects) Put(ctx context.Context, id string, data []byte) error {
	if len(data) == 0 || len(data) > contract.MaxMapImageBytes {
		return auth.ErrUnavailable
	}
	res, err := s.request(ctx, http.MethodPut, id, data)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return auth.ErrUnavailable
	}
	return nil
}
func (s S3Objects) Get(ctx context.Context, id string) ([]byte, error) {
	res, err := s.request(ctx, http.MethodGet, id, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return nil, auth.ErrMissing
	}
	if res.StatusCode != 200 {
		return nil, auth.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, contract.MaxMapImageBytes+1))
	if err != nil || len(data) == 0 || len(data) > contract.MaxMapImageBytes {
		return nil, auth.ErrUnavailable
	}
	return data, nil
}
