package device

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCredentialsHeaderAndCache(t *testing.T) {
	var calls atomic.Int32
	p := &credentialProvider{endpoint: "example", alias: "test", thing: "edge-001", client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Header.Get("x-amzn-iot-thingname") != "edge-001" || r.URL.Path != "/role-aliases/test/credentials" {
			t.Fatal("wrong request")
		}
		body := fmt.Sprintf(`{"credentials":{"accessKeyId":"test-only","secretAccessKey":"test-only","sessionToken":"test-only","expiration":%q}}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	cache := aws.NewCredentialsCache(p)
	for i := 0; i < 2; i++ {
		c, err := cache.Retrieve(context.Background())
		if err != nil || !c.CanExpire || c.Source != "IoTX509" {
			t.Fatal(c.Source, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("credentials not cached")
	}
}
func TestInvalidCredentialsFailClosed(t *testing.T) {
	for _, status := range []int{200, 301, 403, 500} {
		p := &credentialProvider{endpoint: "example", client: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"credentials":{}}`))}, nil
		})}}
		if _, err := p.Retrieve(context.Background()); err == nil {
			t.Fatal("accepted invalid response", status)
		}
	}
}
func TestEndpointValidation(t *testing.T) {
	c := Config{Version: 1, TenantID: "tenant-a", ThingName: "edge-001", PublicKeyHash: strings.Repeat("a", 64), Region: "ap-northeast-1", DataEndpoint: "abc-ats.iot.ap-northeast-1.amazonaws.com", CredentialEndpoint: "abc.credentials.iot.ap-northeast-1.amazonaws.com", TemplateName: "test", RoleAlias: "test", Bucket: "example-bucket"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"https://evil.example", "abc.credentials.iot.ap-northeast-1.amazonaws.com.evil.example", "../x"} {
		c.CredentialEndpoint = bad
		if c.Validate() == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
}
