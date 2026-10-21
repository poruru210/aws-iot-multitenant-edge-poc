package device

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/poruru210/aws-iot-multitenant-edge-poc/internal/identity"
)

type Enrollment struct {
	ThingName     string `json:"thingName"`
	PublicKeyHash string `json:"publicKeyHash"`
}
type Config struct {
	Version            int    `json:"version"`
	TenantID           string `json:"tenantId"`
	ThingName          string `json:"thingName"`
	PublicKeyHash      string `json:"publicKeyHash"`
	Region             string `json:"region"`
	DataEndpoint       string `json:"dataEndpoint"`
	CredentialEndpoint string `json:"credentialEndpoint"`
	TemplateName       string `json:"templateName"`
	RoleAlias          string `json:"roleAlias"`
	Bucket             string `json:"bucket"`
}
type Claim struct {
	Config
	ClaimID        string    `json:"claimId"`
	CertificatePEM string    `json:"certificatePem"`
	PrivateKey     string    `json:"privateKey"`
	Expiration     time.Time `json:"expiration"`
}

var regionRE = regexp.MustCompile(`^[a-z]{2}-[a-z]+-[0-9]+$`)
var bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var hashRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (c Config) Validate() error {
	if c.Version != 1 {
		return errors.New("unsupported config version")
	}
	for _, id := range []string{c.TenantID, c.ThingName, c.TemplateName, c.RoleAlias} {
		if err := identity.ValidateID(id); err != nil {
			return err
		}
	}
	if !regionRE.MatchString(c.Region) || !bucketRE.MatchString(c.Bucket) || !hashRE.MatchString(c.PublicKeyHash) {
		return errors.New("invalid region, bucket or public-key hash")
	}
	// This PoC supports commercial AWS default endpoints, not arbitrary servers.
	for host, suffix := range map[string]string{c.DataEndpoint: "-ats.iot." + c.Region + ".amazonaws.com", c.CredentialEndpoint: ".credentials.iot." + c.Region + ".amazonaws.com"} {
		if !strings.HasSuffix(host, suffix) || !regexp.MustCompile(`^[a-zA-Z0-9-]+$`).MatchString(strings.TrimSuffix(host, suffix)) {
			return errors.New("invalid AWS IoT endpoint")
		}
	}
	return nil
}
func LoadConfig(dir string) (Config, error) {
	var c Config
	if err := identity.ReadJSON(filepath.Join(dir, "config.json"), &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}
func Initialize(dir, thing string) error {
	key, csr, hash, err := identity.Generate(thing)
	if err != nil {
		return err
	}
	if err = identity.NewPrivate(filepath.Join(dir, "device.key"), key); err != nil {
		return err
	}
	if err = identity.NewPrivate(filepath.Join(dir, "device.csr"), csr); err != nil {
		return err
	}
	return identity.AtomicPrivate(filepath.Join(dir, "enrollment.json"), Enrollment{ThingName: thing, PublicKeyHash: hash})
}

// IssueClaim is run on the technician's machine; only this command loads the
// operator's default AWS/SSO credential chain. Device upload commands do NOT.
func IssueClaim(ctx context.Context, apiURL, region, tenant, requestFile, output string) error {
	if err := identity.ValidateID(tenant); err != nil {
		return err
	}
	var enrollment Enrollment
	if err := identity.ReadJSON(requestFile, &enrollment); err != nil {
		return err
	}
	if err := identity.ValidateID(enrollment.ThingName); err != nil {
		return err
	}
	if !hashRE.MatchString(enrollment.PublicKeyHash) {
		return errors.New("invalid public-key hash")
	}
	u, err := url.Parse(apiURL)
	if err != nil {
		return err
	}
	suffix := ".execute-api." + region + ".amazonaws.com"
	if !regionRE.MatchString(region) || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" || !strings.HasSuffix(u.Hostname(), suffix) || !regexp.MustCompile(`^[a-z0-9]+$`).MatchString(strings.TrimSuffix(u.Hostname(), suffix)) {
		return errors.New("api-url must be the deployed AWS HTTPS REST API URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/tenants/" + tenant + "/claims"
	data, err := json.Marshal(enrollment)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return err
	}
	credentials, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if err = v4.NewSigner().SignHTTP(ctx, credentials, req, hex.EncodeToString(sum[:]), "execute-api", region, time.Now()); err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("claim API returned HTTP %d; inspect backend logs without logging credentials", resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil {
		return err
	}
	if len(data) > 64*1024 {
		return errors.New("claim response too large")
	}
	var claim Claim
	if err = json.Unmarshal(data, &claim); err != nil {
		return err
	}
	if err = claim.Config.Validate(); err != nil {
		return err
	}
	if claim.ThingName != enrollment.ThingName || claim.TenantID != tenant || claim.PublicKeyHash != enrollment.PublicKeyHash || claim.Region != region || !claim.Expiration.After(time.Now()) {
		return errors.New("claim response does not match enrollment")
	}
	return identity.NewPrivate(output, data)
}

func readPrivate(dir, name string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, name)) }
