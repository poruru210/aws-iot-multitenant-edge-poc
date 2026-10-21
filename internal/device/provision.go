package device

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/poruru210/aws-iot-multitenant-edge-poc/internal/identity"
)

func tlsConfig(cert, key []byte, caFile, server string) (*tls.Config, error) {
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if caFile != "" {
		ca, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		if !roots.AppendCertsFromPEM(ca) {
			return nil, errors.New("CA file contains no valid certificates")
		}
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: server, RootCAs: roots, Certificates: []tls.Certificate{pair}}, nil
}

type pendingCertificate struct {
	ClaimID        string `json:"claimId"`
	CertificateID  string `json:"certificateId"`
	CertificatePEM string `json:"certificatePem"`
	OwnershipToken string `json:"certificateOwnershipToken"`
}

func wait(ctx context.Context, token mqtt.Token) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-token.Done():
		return token.Error()
	}
}
func rpc(ctx context.Context, client mqtt.Client, topic string, request any, response any) error {
	type result struct {
		data     []byte
		rejected bool
	}
	messages := make(chan result, 1)
	handler := func(_ mqtt.Client, m mqtt.Message) {
		select {
		case messages <- result{append([]byte(nil), m.Payload()...), m.Topic() == topic+"/rejected"}:
		default:
		}
	}
	if err := wait(ctx, client.SubscribeMultiple(map[string]byte{topic + "/accepted": 1, topic + "/rejected": 1}, handler)); err != nil {
		return err
	}
	defer client.Unsubscribe(topic+"/accepted", topic+"/rejected")
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if err = wait(ctx, client.Publish(topic, 1, false, payload)); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-messages:
		if result.rejected {
			var rejection struct {
				ErrorCode  string `json:"errorCode"`
				StatusCode int    `json:"statusCode"`
			}
			_ = json.Unmarshal(result.data, &rejection)
			return fmt.Errorf("fleet provisioning rejected: %s (%d)", rejection.ErrorCode, rejection.StatusCode)
		}
		return json.Unmarshal(result.data, response)
	}
}

func Provision(parent context.Context, dir, claimFile, caFile string, port int) error {
	if port != 8883 && port != 443 {
		return errors.New("MQTT port must be 8883 or 443")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err == nil {
		return errors.New("device already configured; refusing automatic re-registration")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var claim Claim
	if err := identity.ReadJSON(claimFile, &claim); err != nil {
		return err
	}
	if err := claim.Config.Validate(); err != nil {
		return err
	}
	if time.Until(claim.Expiration) < 15*time.Second {
		return errors.New("claim is expired or too close to expiry")
	}
	ctx, cancel := context.WithDeadline(parent, claim.Expiration.Add(-5*time.Second))
	defer cancel()
	key, err := readPrivate(dir, "device.key")
	if err != nil {
		return err
	}
	csr, err := readPrivate(dir, "device.csr")
	if err != nil {
		return err
	}
	hash, err := identity.CSRHash(csr)
	if err != nil || hash != claim.PublicKeyHash {
		return errors.New("CSR does not match authorized public key")
	}
	var enrollment Enrollment
	if err = identity.ReadJSON(filepath.Join(dir, "enrollment.json"), &enrollment); err != nil {
		return err
	}
	if enrollment.ThingName != claim.ThingName || enrollment.PublicKeyHash != hash {
		return errors.New("claim does not match device enrollment")
	}
	tlsCfg, err := tlsConfig([]byte(claim.CertificatePEM), []byte(claim.PrivateKey), caFile, claim.DataEndpoint)
	if err != nil {
		return err
	}
	if port == 443 {
		tlsCfg.NextProtos = []string{"x-amzn-mqtt-ca"}
	}
	nonce := make([]byte, 8)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	options := mqtt.NewClientOptions().AddBroker(fmt.Sprintf("ssl://%s:%d", claim.DataEndpoint, port)).SetClientID("bootstrap-" + hex.EncodeToString(nonce)).SetTLSConfig(tlsCfg)
	options.SetProtocolVersion(4).SetCleanSession(true).SetAutoReconnect(false).SetConnectRetry(false).SetConnectTimeout(15 * time.Second).SetWriteTimeout(15 * time.Second)
	options.SetConnectionLostHandler(func(mqtt.Client, error) { cancel() })
	client := mqtt.NewClient(options)
	if err = wait(ctx, client.Connect()); err != nil {
		return err
	}
	defer client.Disconnect(250)
	pendingPath := filepath.Join(dir, "pending.json")
	var pending pendingCertificate
	if err = identity.ReadJSON(pendingPath, &pending); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = rpc(ctx, client, "$aws/certificates/create-from-csr/json", map[string]string{"certificateSigningRequest": string(csr)}, &pending); err != nil {
			return err
		}
		pending.ClaimID = claim.ClaimID
		if pending.CertificateID == "" || pending.CertificatePEM == "" || pending.OwnershipToken == "" {
			return errors.New("incomplete certificate response")
		}
		if err = identity.AtomicPrivate(pendingPath, pending); err != nil {
			return err
		}
	}
	if pending.ClaimID != claim.ClaimID {
		return errors.New("pending certificate belongs to a different claim; operator reconciliation required")
	}
	if _, err = tls.X509KeyPair([]byte(pending.CertificatePEM), key); err != nil {
		return errors.New("issued certificate does not match local private key")
	}
	var registered struct {
		ThingName string `json:"thingName"`
	}
	if err = rpc(ctx, client, "$aws/provisioning-templates/"+claim.TemplateName+"/provision/json", map[string]any{
		"certificateOwnershipToken": pending.OwnershipToken,
		"parameters":                map[string]string{"TenantId": claim.TenantID, "ThingName": claim.ThingName},
	}, &registered); err != nil {
		return err
	}
	if registered.ThingName != claim.ThingName {
		return errors.New("registered Thing name mismatch")
	}
	certPath := filepath.Join(dir, "device.crt")
	if existing, readErr := os.ReadFile(certPath); readErr == nil {
		if !bytes.Equal(existing, []byte(pending.CertificatePEM)) {
			return errors.New("refusing to replace a different certificate")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	} else if err = identity.NewPrivate(certPath, []byte(pending.CertificatePEM)); err != nil {
		return err
	}
	if err = identity.AtomicPrivate(filepath.Join(dir, "config.json"), claim.Config); err != nil {
		return err
	}
	return os.Remove(pendingPath)
}
