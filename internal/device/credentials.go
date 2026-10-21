package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type credentialProvider struct {
	client                 *http.Client
	endpoint, alias, thing string
}

func (p *credentialProvider) Retrieve(ctx context.Context) (aws.Credentials, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+p.endpoint+"/role-aliases/"+p.alias+"/credentials", nil)
	if err != nil {
		return aws.Credentials{}, err
	}
	req.Header.Set("x-amzn-iot-thingname", p.thing)
	resp, err := p.client.Do(req)
	if err != nil {
		return aws.Credentials{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return aws.Credentials{}, fmt.Errorf("IoT credentials provider HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024+1))
	if err != nil {
		return aws.Credentials{}, err
	}
	if len(body) > 32*1024 {
		return aws.Credentials{}, errors.New("credentials response too large")
	}
	var result struct {
		Credentials struct {
			AccessKeyID     string    `json:"accessKeyId"`
			SecretAccessKey string    `json:"secretAccessKey"`
			SessionToken    string    `json:"sessionToken"`
			Expiration      time.Time `json:"expiration"`
		} `json:"credentials"`
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return aws.Credentials{}, errors.New("invalid credentials response")
	}
	c := result.Credentials
	if c.AccessKeyID == "" || c.SecretAccessKey == "" || c.SessionToken == "" || time.Until(c.Expiration) < time.Minute {
		return aws.Credentials{}, errors.New("missing or expired temporary credentials")
	}
	return aws.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, CanExpire: true, Expires: c.Expiration, Source: "IoTX509"}, nil
}

func Clients(dir, caFile string) (Config, *aws.CredentialsCache, *s3.Client, error) {
	c, err := LoadConfig(dir)
	if err != nil {
		return c, nil, nil, err
	}
	cert, err := readPrivate(dir, "device.crt")
	if err != nil {
		return c, nil, nil, err
	}
	key, err := readPrivate(dir, "device.key")
	if err != nil {
		return c, nil, nil, err
	}
	tlsCfg, err := tlsConfig(cert, key, caFile, c.CredentialEndpoint)
	if err != nil {
		return c, nil, nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsCfg
	provider := &credentialProvider{client: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint: c.CredentialEndpoint, alias: c.RoleAlias, thing: c.ThingName}
	cache := aws.NewCredentialsCache(provider, func(o *aws.CredentialsCacheOptions) { o.ExpiryWindow = 2 * time.Minute; o.ExpiryWindowJitterFrac = 0.2 })
	// Do not load a default config here: a developer's admin credentials or custom
	// service endpoints must never mask an isolation failure in the device test.
	cfg := aws.Config{Region: c.Region, Credentials: cache, Retryer: func() aws.Retryer { return retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = 5 }) }}
	return c, cache, s3.NewFromConfig(cfg), nil
}
