package identity

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestIDs(t *testing.T) {
	for _, s := range []string{"tenant-a", "edge_001", "A"} {
		if err := ValidateID(s); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"", "../other", "a/b", "*", "a?", "${tenant}", "a\n", "顧客"} {
		if ValidateID(s) == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
func TestObjectKey(t *testing.T) {
	key, err := ObjectKey("tenant-a", "edge-001", "2026/10/data.bin")
	if err != nil || key != "tenants/tenant-a/devices/edge-001/2026/10/data.bin" {
		t.Fatalf("%s %v", key, err)
	}
	for _, s := range []string{"../b", "/etc", "a//b", "a/./b", "a\\b", "", "x\n"} {
		if _, e := ObjectKey("a", "b", s); e == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
func TestGenerateAndCSR(t *testing.T) {
	key, csr, hash, err := Generate("edge-001")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(key)
	if _, err = x509.ParsePKCS8PrivateKey(block.Bytes); err != nil {
		t.Fatal(err)
	}
	got, err := CSRHash(csr)
	if err != nil || got != hash || len(hash) != 64 {
		t.Fatalf("%s %s %v", got, hash, err)
	}
	if _, err = CSRHash([]byte("bad")); err == nil {
		t.Fatal("invalid CSR accepted")
	}
}
func TestPrivateFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := NewPrivate(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := NewPrivate(path, []byte("second")); err == nil {
		t.Fatal("overwrote existing key")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "first" {
		t.Fatal("key changed")
	}
	path = filepath.Join(t.TempDir(), "state.json")
	if err := AtomicPrivate(path, map[string]int{"version": 1}); err != nil {
		t.Fatal(err)
	}
	if err := AtomicPrivate(path, map[string]int{"version": 2}); err != nil {
		t.Fatal(err)
	}
	var state map[string]int
	if err := ReadJSON(path, &state); err != nil || state["version"] != 2 {
		t.Fatal(state, err)
	}
}
