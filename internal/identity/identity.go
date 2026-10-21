// Package identity contains offline-testable key and path validation helpers.
package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9._=-]+$`)

func ValidateID(s string) error {
	if !idPattern.MatchString(s) {
		return fmt.Errorf("invalid identifier %q: use 1-64 ASCII letters, numbers, '_' or '-'", s)
	}
	return nil
}

func ObjectKey(tenant, thing, relative string) (string, error) {
	if err := ValidateID(tenant); err != nil {
		return "", err
	}
	if err := ValidateID(thing); err != nil {
		return "", err
	}
	if len(relative) == 0 || len(relative) > 700 {
		return "", errors.New("relative key must be 1-700 bytes")
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "." || part == ".." || !segmentPattern.MatchString(part) {
			return "", errors.New("unsafe relative object key")
		}
	}
	return "tenants/" + tenant + "/devices/" + thing + "/" + relative, nil
}

func PublicKeyHash(key any) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}

func Generate(thing string) (keyPEM, csrPEM []byte, hash string, err error) {
	if err = ValidateID(thing); err != nil {
		return
	}
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		err = e
		return
	}
	der, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		err = e
		return
	}
	csr, e := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: thing}}, key)
	if e != nil {
		err = e
		return
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	csrPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})
	hash, err = PublicKeyHash(key.Public())
	return
}

func CSRHash(data []byte) (string, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return "", errors.New("invalid CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return "", err
	}
	if err = csr.CheckSignature(); err != nil {
		return "", err
	}
	return PublicKeyHash(csr.PublicKey)
}

// NewPrivate never replaces an existing file, especially an existing private key.
func NewPrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// AtomicPrivate writes a same-directory temporary file and atomically renames it.
// Windows callers must additionally configure NTFS ACLs; chmod is not an ACL.
func AtomicPrivate(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".edge-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func ReadJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > 128*1024 {
		return errors.New("configuration file exceeds 128 KiB")
	}
	return json.Unmarshal(data, out)
}
