package cli

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestWriteAndReadEncryptedTLSCAPrivateKeyFile(t *testing.T) {
	keyring.MockInit()
	key := generateTestRSAKey(t)
	path := filepath.Join(t.TempDir(), serveTLSCAKeyName)

	if err := writeTLSCAPrivateKeyFile(path, key, tlsCAKeyStorageOptions{Policy: tlsCAKeyStorageEncryptedRequired}); err != nil {
		t.Fatalf("writeTLSCAPrivateKeyFile() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(key) error = %v", err)
	}
	if strings.Contains(string(data), "-----BEGIN RSA PRIVATE KEY-----") {
		t.Fatalf("encrypted key file contains plaintext PEM type: %q", string(data))
	}
	if !strings.Contains(string(data), tlsCAEncryptedKeyPEMType) {
		t.Fatalf("encrypted key file = %q, want encrypted PEM type", string(data))
	}

	got, err := readTLSCAPrivateKeyFile(path, tlsCAKeyStorageOptions{Policy: tlsCAKeyStorageFlexible})
	if err != nil {
		t.Fatalf("readTLSCAPrivateKeyFile() error = %v", err)
	}
	if got.N.Cmp(key.N) != 0 || got.D.Cmp(key.D) != 0 {
		t.Fatal("readTLSCAPrivateKeyFile() returned a different private key")
	}
}

func TestReadTLSCAPrivateKeyFileSupportsPlaintextInFlexibleMode(t *testing.T) {
	key := generateTestRSAKey(t)
	path := filepath.Join(t.TempDir(), serveTLSCAKeyName)
	if err := writePEMFile(path, 0o600, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key)); err != nil {
		t.Fatalf("writePEMFile() error = %v", err)
	}

	got, err := readTLSCAPrivateKeyFile(path, tlsCAKeyStorageOptions{Policy: tlsCAKeyStorageFlexible})
	if err != nil {
		t.Fatalf("readTLSCAPrivateKeyFile() error = %v", err)
	}
	if got.N.Cmp(key.N) != 0 || got.D.Cmp(key.D) != 0 {
		t.Fatal("readTLSCAPrivateKeyFile() returned a different private key")
	}
}

func TestWriteTLSCAPrivateKeyFileEncryptedRequiredFailsWhenKeyringFails(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keyring"))
	t.Cleanup(keyring.MockInit)
	key := generateTestRSAKey(t)
	path := filepath.Join(t.TempDir(), serveTLSCAKeyName)

	err := writeTLSCAPrivateKeyFile(path, key, tlsCAKeyStorageOptions{Policy: tlsCAKeyStorageEncryptedRequired})
	if err == nil || !strings.Contains(err.Error(), "no keyring") {
		t.Fatalf("writeTLSCAPrivateKeyFile() error = %v, want keyring error", err)
	}
}

func TestMaterializeTLSPrivateKeyFileWritesPlaintextRuntimeCopy(t *testing.T) {
	keyring.MockInit()
	key := generateTestRSAKey(t)
	sourcePath := filepath.Join(t.TempDir(), "cache", serveTLSKeyFileName)
	targetPath := filepath.Join(t.TempDir(), "run", serveTLSKeyFileName)
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("MkdirAll(source dir) error = %v", err)
	}
	if err := writeTLSPrivateKeyFile(sourcePath, key, tlsCAKeyStorageOptions{Policy: tlsCAKeyStorageEncryptedRequired}); err != nil {
		t.Fatalf("writeTLSPrivateKeyFile() error = %v", err)
	}

	if err := materializeTLSPrivateKeyFile(sourcePath, targetPath, tlsCAKeyStorageOptions{Policy: tlsCAKeyStorageFlexible}); err != nil {
		t.Fatalf("materializeTLSPrivateKeyFile() error = %v", err)
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile(runtime key) error = %v", err)
	}
	if !strings.Contains(string(data), "-----BEGIN RSA PRIVATE KEY-----") {
		t.Fatalf("runtime key = %q, want plaintext RSA PRIVATE KEY", string(data))
	}
}

func TestWriteTLSCAPrivateKeyFilePlaintextRequiredIgnoresKeyring(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keyring"))
	t.Cleanup(keyring.MockInit)
	key := generateTestRSAKey(t)
	path := filepath.Join(t.TempDir(), serveTLSCAKeyName)

	if err := writeTLSCAPrivateKeyFile(path, key, tlsCAKeyStorageOptions{Policy: tlsCAKeyStoragePlaintextRequired}); err != nil {
		t.Fatalf("writeTLSCAPrivateKeyFile() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(key) error = %v", err)
	}
	if !strings.Contains(string(data), "-----BEGIN RSA PRIVATE KEY-----") {
		t.Fatalf("plaintext key file = %q, want RSA PRIVATE KEY PEM", string(data))
	}
}

func TestWriteTLSCAPrivateKeyFileFlexibleFallsBackToPlaintext(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keyring"))
	t.Cleanup(keyring.MockInit)
	key := generateTestRSAKey(t)
	path := filepath.Join(t.TempDir(), serveTLSCAKeyName)
	warnings := []string{}

	err := writeTLSCAPrivateKeyFile(path, key, tlsCAKeyStorageOptions{
		Policy: tlsCAKeyStorageFlexible,
		Warnf: func(format string, args ...any) {
			warnings = append(warnings, strings.TrimSpace(format))
		},
	})
	if err != nil {
		t.Fatalf("writeTLSCAPrivateKeyFile() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(key) error = %v", err)
	}
	if !strings.Contains(string(data), "-----BEGIN RSA PRIVATE KEY-----") {
		t.Fatalf("fallback key file = %q, want plaintext RSA PRIVATE KEY PEM", string(data))
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "OS keyring is unavailable") {
		t.Fatalf("warnings = %#v, want keyring fallback warning", warnings)
	}
}

func generateTestRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error = %v", err)
	}

	return key
}
