package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/poruru210/aws-iot-multitenant-edge-poc/internal/device"
	"github.com/poruru210/aws-iot-multitenant-edge-poc/internal/identity"
)

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: edge {init|claim|provision|credentials|upload|probe-denied} --help")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dir := f.String("dir", ".private/device", "device state directory (private)")
	ca := f.String("ca", "", "optional additional PEM CA file; TLS verification is never disabled")
	switch args[0] {
	case "init":
		thing := f.String("thing", "", "AWS IoT Thing name")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if err := device.Initialize(*dir, *thing); err != nil {
			return err
		}
		fmt.Println("Key and CSR created locally. Transfer only enrollment.json to the technician.")
	case "claim":
		api := f.String("api-url", "", "RegistrationApiUrl CDK output")
		region := f.String("region", "ap-northeast-1", "AWS region")
		tenant := f.String("tenant", "", "authorized tenant ID")
		request := f.String("request", "", "enrollment.json from the device (no private key)")
		out := f.String("out", ".private/claim.json", "new secret claim bundle file")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if err := device.IssueClaim(ctx, *api, *region, *tenant, *request, *out); err != nil {
			return err
		}
		fmt.Println("Claim saved. Transfer securely and finish provisioning before its five-minute expiration.")
	case "provision":
		claim := f.String("claim", ".private/claim.json", "secret claim bundle")
		port := f.Int("mqtt-port", 8883, "MQTT/TLS port: 8883 or 443 (ALPN)")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if err := device.Provision(ctx, *dir, *claim, *ca, *port); err != nil {
			return err
		}
		fmt.Println("Provisioned. Securely delete all copies of the temporary claim bundle.")
	case "credentials":
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		c, cache, _, err := device.Clients(*dir, *ca)
		if err != nil {
			return err
		}
		creds, err := cache.Retrieve(ctx)
		if err != nil {
			return err
		}
		// Never print the access key, secret key or session token.
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"tenantId": c.TenantID, "thingName": c.ThingName, "expiration": creds.Expires.UTC().Format(time.RFC3339), "source": creds.Source})
	case "upload":
		file := f.String("file", "", "immutable local file")
		key := f.String("key", "", "relative object key under this device's prefix")
		state := f.String("state", ".private/upload.json", "unique checkpoint for this file")
		part := f.Int64("part-mib", 64, "multipart size in MiB (5-512)")
		parallel := f.Int("parallel", 4, "concurrent parts (1-16)")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		c, _, client, err := device.Clients(*dir, *ca)
		if err != nil {
			return err
		}
		etag, err := device.Upload(ctx, client, c, *file, *key, *state, *part*1024*1024, *parallel)
		if err != nil {
			return err
		}
		fmt.Printf("Upload complete (or previously recorded complete). ETag=%s\n", etag)
	case "probe-denied":
		tenant := f.String("target-tenant", "", "different tenant, or own tenant with a different Thing")
		thing := f.String("target-thing", "", "target Thing for negative IAM test")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		c, _, client, err := device.Clients(*dir, *ca)
		if err != nil {
			return err
		}
		if *tenant == c.TenantID && *thing == c.ThingName {
			return errors.New("negative test must target another device or tenant")
		}
		nonce := make([]byte, 8)
		if _, err = rand.Read(nonce); err != nil {
			return err
		}
		key, err := identity.ObjectKey(*tenant, *thing, "__isolation-probe-"+hex.EncodeToString(nonce)+".txt")
		if err != nil {
			return err
		}
		_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: &c.Bucket, Key: &key, Body: strings.NewReader("isolation-test\n")})
		if err == nil {
			return fmt.Errorf("ISOLATION FAILURE: unexpected object created at s3://%s/%s; operator cleanup required", c.Bucket, key)
		}
		var apiError interface {
			error
			ErrorCode() string
		}
		if errors.As(err, &apiError) && apiError.ErrorCode() == "AccessDenied" {
			fmt.Println("Expected AccessDenied received. Run a successful own-prefix upload as the positive control.")
			return nil
		}
		return fmt.Errorf("negative test inconclusive (not AccessDenied): %w", err)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	return nil
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
