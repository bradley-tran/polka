package cli

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// stubCertificateTrustStore isolates command tests from real trust stores on
// every host OS, including Linux preflight authentication.
func stubCertificateTrustStore(t *testing.T) *certificateTrustStore {
	t.Helper()
	store := &certificateTrustStore{
		install: func(string) (string, error) { return "test", nil },
		remove:  func(*x509.Certificate) (string, error) { return "test", nil },
	}
	previous := prepareCertificateTrustStoreFunc
	prepareCertificateTrustStoreFunc = func(io.Writer) (certificateTrustStore, error) {
		return *store, nil
	}
	t.Cleanup(func() { prepareCertificateTrustStoreFunc = previous })
	return store
}

// TestRunCertInstallCreatesAndInstallsGlobalCertificate checks generation,
// encrypted private-key storage, and the public certificate passed to install.
func TestRunCertInstallCreatesAndInstallsGlobalCertificate(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	trustStore := stubCertificateTrustStore(t)

	installCalls := 0
	installedPath := ""
	trustStore.install = func(certificatePath string) (string, error) {
		installCalls++
		installedPath = certificatePath
		return "test", nil
	}

	if code := Run(stdout, stderr, []string{"--root", root, "cert-install"}); code != 0 {
		t.Fatalf("Run(cert-install) code = %d, stderr = %q", code, stderr.String())
	}
	if installCalls != 1 {
		t.Fatalf("install calls = %d, want 1", installCalls)
	}
	wantPath := filepath.Join(cacheDir, "polka", serveTLSSubdir, serveTLSCACertName)
	if installedPath != wantPath {
		t.Fatalf("installed path = %q, want global ca certificate path %q", installedPath, wantPath)
	}
	if _, err := os.Stat(installedPath); err != nil {
		t.Fatalf("Stat(installed ca cert) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "polka", serveTLSSubdir, serveTLSCAKeyName)); err != nil {
		t.Fatalf("Stat(global ca key) error = %v", err)
	}
	caKeyData, err := os.ReadFile(filepath.Join(cacheDir, "polka", serveTLSSubdir, serveTLSCAKeyName))
	if err != nil {
		t.Fatalf("ReadFile(global ca key) error = %v", err)
	}
	if strings.Contains(string(caKeyData), "-----BEGIN RSA PRIVATE KEY-----") {
		t.Fatalf("global ca key = %q, want encrypted key", string(caKeyData))
	}
	if !strings.Contains(string(caKeyData), tlsCAEncryptedKeyPEMType) {
		t.Fatalf("global ca key = %q, want encrypted key PEM type", string(caKeyData))
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "polka", serveTLSSubdir, serveTLSCertFileName)); err != nil {
		t.Fatalf("Stat(global server cert) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "polka", serveTLSSubdir, serveTLSKeyFileName)); err != nil {
		t.Fatalf("Stat(global server key) error = %v", err)
	}
	serverKeyData, err := os.ReadFile(filepath.Join(cacheDir, "polka", serveTLSSubdir, serveTLSKeyFileName))
	if err != nil {
		t.Fatalf("ReadFile(global server key) error = %v", err)
	}
	if strings.Contains(string(serverKeyData), "-----BEGIN RSA PRIVATE KEY-----") {
		t.Fatalf("global server key = %q, want encrypted key", string(serverKeyData))
	}
	if !strings.Contains(string(serverKeyData), tlsEncryptedPrivateKeyPEMType) {
		t.Fatalf("global server key = %q, want encrypted key PEM type", string(serverKeyData))
	}
	output := stdout.String()
	if !strings.Contains(output, "Installed Polka local HTTPS CA certificate into the test trust store.") {
		t.Fatalf("Run(cert-install) stdout = %q, want install summary", output)
	}
	if !strings.Contains(output, "CA certificate: "+installedPath) {
		t.Fatalf("Run(cert-install) stdout = %q, want ca certificate path", output)
	}
}

// TestRunCertInstallRegeneratesExistingGlobalCertificate checks that malformed
// legacy cached material can still be replaced by a fresh certificate pair.
func TestRunCertInstallRegeneratesExistingGlobalCertificate(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	caCertificatePath := filepath.Join(cacheDir, "polka", serveTLSSubdir, serveTLSCACertName)
	caKeyPath := filepath.Join(cacheDir, "polka", serveTLSSubdir, serveTLSCAKeyName)
	serverCertificatePath := filepath.Join(cacheDir, "polka", serveTLSSubdir, serveTLSCertFileName)
	serverKeyPath := filepath.Join(cacheDir, "polka", serveTLSSubdir, serveTLSKeyFileName)
	if err := os.MkdirAll(filepath.Dir(caCertificatePath), 0o755); err != nil {
		t.Fatalf("MkdirAll(cert dir) error = %v", err)
	}
	for path, contents := range map[string]string{
		caCertificatePath:     "old ca certificate",
		caKeyPath:             "old ca key",
		serverCertificatePath: "old server certificate",
		serverKeyPath:         "old server key",
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	stubCertificateTrustStore(t)

	if code := Run(stdout, stderr, []string{"--root", root, "cert-install"}); code != 0 {
		t.Fatalf("Run(cert-install) code = %d, stderr = %q", code, stderr.String())
	}
	certificateData, err := os.ReadFile(caCertificatePath)
	if err != nil {
		t.Fatalf("ReadFile(ca cert) error = %v", err)
	}
	if strings.Contains(string(certificateData), "old ca certificate") {
		t.Fatalf("ca certificate data = %q, want regenerated certificate", string(certificateData))
	}
	block, _ := pem.Decode(certificateData)
	if block == nil {
		t.Fatal("pem.Decode(regenerated ca cert) = nil")
	}
}

// TestRunCertInstallRemovesExistingCAFromTrustStoreBeforeRegeneration checks
// that preparation, old-CA removal, and new-CA installation remain ordered.
func TestRunCertInstallRemovesExistingCAFromTrustStoreBeforeRegeneration(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	if _, _, err := regenerateGlobalTLSCertificateWithOptions(cacheDir, tlsCAKeyStorageOptions{Policy: tlsCAKeyStoragePlaintextRequired}); err != nil {
		t.Fatalf("regenerateGlobalTLSCertificateWithOptions() error = %v", err)
	}
	caCertificatePath, _ := globalTLSCACertificatePaths(cacheDir)
	oldCertificate, err := readCertificateFile(caCertificatePath)
	if err != nil {
		t.Fatalf("readCertificateFile(old ca) error = %v", err)
	}
	oldThumbprint := certificateSHA1Thumbprint(oldCertificate)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	trustStore := stubCertificateTrustStore(t)
	events := []string{}
	prepareCertificateTrustStoreFunc = func(io.Writer) (certificateTrustStore, error) {
		events = append(events, "prepare")
		return *trustStore, nil
	}
	trustStore.remove = func(certificate *x509.Certificate) (string, error) {
		events = append(events, "remove")
		if got := certificateSHA1Thumbprint(certificate); got != oldThumbprint {
			t.Fatalf("removed certificate thumbprint = %q, want old CA %q", got, oldThumbprint)
		}
		cachedCertificate, err := readCertificateFile(caCertificatePath)
		if err != nil {
			t.Fatalf("readCertificateFile(cached ca during removal) error = %v", err)
		}
		if got := certificateSHA1Thumbprint(cachedCertificate); got != oldThumbprint {
			t.Fatalf("cached certificate thumbprint during removal = %q, want old CA %q", got, oldThumbprint)
		}

		return "test", nil
	}
	trustStore.install = func(certificatePath string) (string, error) {
		events = append(events, "install")
		newCertificate, err := readCertificateFile(certificatePath)
		if err != nil {
			t.Fatalf("readCertificateFile(new ca) error = %v", err)
		}
		if got := certificateSHA1Thumbprint(newCertificate); got == oldThumbprint {
			t.Fatalf("installed certificate thumbprint = %q, want regenerated CA", got)
		}

		return "test", nil
	}

	if code := Run(stdout, stderr, []string{"--root", root, "cert-install"}); code != 0 {
		t.Fatalf("Run(cert-install) code = %d, stderr = %q", code, stderr.String())
	}
	if got := strings.Join(events, ","); got != "prepare,remove,install" {
		t.Fatalf("trust-store events = %q, want prepare,remove,install", got)
	}
}

// TestRunCertInstallKeyringFailurePreservesCertificates checks that keyring
// failures are actionable and leave cached certificates and trust entries alone.
func TestRunCertInstallKeyringFailurePreservesCertificates(t *testing.T) {
	for _, test := range []struct {
		name             string
		existing         bool
		invalidServerKey bool
	}{
		{name: "unavailable keyring on first install"},
		{name: "unavailable keyring with existing certificates", existing: true},
		{name: "invalid server encryption key", existing: true, invalidServerKey: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cacheDir := filepath.Join(t.TempDir(), "global-cache")
			t.Setenv("POLKA_CACHE_DIR", cacheDir)
			t.Cleanup(keyring.MockInit)
			caCertPath, caKeyPath := globalTLSCACertificatePaths(cacheDir)
			certPath, keyPath := globalTLSCertificatePaths(cacheDir)
			originalFiles := make(map[string][]byte)
			if test.existing {
				if _, _, err := regenerateGlobalTLSCertificateWithOptions(cacheDir, tlsCAKeyStorageOptions{Policy: tlsCAKeyStoragePlaintextRequired}); err != nil {
					t.Fatalf("regenerateGlobalTLSCertificateWithOptions() error = %v", err)
				}
				for _, path := range []string{caCertPath, caKeyPath, certPath, keyPath} {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatalf("ReadFile(%s) error = %v", path, err)
					}
					originalFiles[path] = data
				}
			}

			keyringError := "failed to unlock correct collection '/org/freedesktop/secrets/aliases/default'"
			if test.invalidServerKey {
				keyring.MockInit()
				keyID, err := tlsKeyringAccount(keyPath)
				if err != nil {
					t.Fatalf("tlsKeyringAccount() error = %v", err)
				}
				if err := keyring.Set(tlsPrivateKeyringService, keyID, "invalid base64"); err != nil {
					t.Fatalf("keyring.Set() error = %v", err)
				}
				keyringError = "decode local HTTPS private key encryption key"
			} else {
				keyring.MockInitWithError(errors.New(keyringError))
			}

			stubCertificateTrustStore(t)
			prepareCertificateTrustStoreFunc = func(io.Writer) (certificateTrustStore, error) {
				t.Fatal("trust-store preparation called after keyring failure")
				return certificateTrustStore{}, nil
			}

			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			if code := Run(stdout, stderr, []string{"cert-install"}); code != 1 {
				t.Fatalf("Run(cert-install) code = %d, want 1", code)
			}
			for _, message := range []string{keyringError, "unlock", "polka cert-install --no-encryption", "plaintext"} {
				if !strings.Contains(stderr.String(), message) {
					t.Errorf("stderr = %q, want %q", stderr.String(), message)
				}
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want no success output", stdout.String())
			}
			for path, original := range originalFiles {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("ReadFile(%s) after failure: %v", path, err)
				}
				if !bytes.Equal(data, original) {
					t.Errorf("certificate file %s changed after keyring failure", path)
				}
			}
			if !test.existing {
				if _, err := os.Stat(filepath.Dir(caCertPath)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("Stat(cert directory) error = %v, want not exist", err)
				}
			}
		})
	}
}

// TestRunCertInstallNoEncryptionCreatesPlaintextCAKey verifies that the explicit
// plaintext option works even when the OS keyring cannot be accessed.
func TestRunCertInstallNoEncryptionCreatesPlaintextCAKey(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keyring"))
	t.Cleanup(keyring.MockInit)
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	stubCertificateTrustStore(t)

	if code := Run(stdout, stderr, []string{"--root", root, "cert-install", "--no-encryption"}); code != 0 {
		t.Fatalf("Run(cert-install --no-encryption) code = %d, stderr = %q", code, stderr.String())
	}
	caKeyPath := filepath.Join(cacheDir, "polka", serveTLSSubdir, serveTLSCAKeyName)
	caKeyData, err := os.ReadFile(caKeyPath)
	if err != nil {
		t.Fatalf("ReadFile(global ca key) error = %v", err)
	}
	if !strings.Contains(string(caKeyData), "-----BEGIN RSA PRIVATE KEY-----") {
		t.Fatalf("global ca key = %q, want plaintext RSA PRIVATE KEY", string(caKeyData))
	}
	serverKeyPath := filepath.Join(cacheDir, "polka", serveTLSSubdir, serveTLSKeyFileName)
	serverKeyData, err := os.ReadFile(serverKeyPath)
	if err != nil {
		t.Fatalf("ReadFile(global server key) error = %v", err)
	}
	if !strings.Contains(string(serverKeyData), "-----BEGIN RSA PRIVATE KEY-----") {
		t.Fatalf("global server key = %q, want plaintext RSA PRIVATE KEY", string(serverKeyData))
	}
	if !strings.Contains(stderr.String(), "warning: writing Polka local HTTPS private keys without OS keyring encryption") {
		t.Fatalf("Run(cert-install --no-encryption) stderr = %q, want plaintext warning", stderr.String())
	}
}

// TestRunCertInstallTrustStoreFailures checks that preparation and removal
// failures preserve old files, while late failures retain generated material.
func TestRunCertInstallTrustStoreFailures(t *testing.T) {
	for _, test := range []struct {
		phase    string
		existing bool
	}{
		{phase: "prepare"},
		{phase: "prepare", existing: true},
		{phase: "remove", existing: true},
		{phase: "install", existing: true},
	} {
		t.Run(fmt.Sprintf("%s existing=%v", test.phase, test.existing), func(t *testing.T) {
			cache := t.TempDir()
			t.Setenv("POLKA_CACHE_DIR", cache)
			caPath, caKeyPath := globalTLSCACertificatePaths(cache)
			serverPath, serverKeyPath := globalTLSCertificatePaths(cache)
			originals := make(map[string][]byte)
			if test.existing {
				if _, _, err := regenerateGlobalTLSCertificateWithOptions(cache, tlsCAKeyStorageOptions{Policy: tlsCAKeyStoragePlaintextRequired}); err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{caPath, caKeyPath, serverPath, serverKeyPath} {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					originals[path] = data
				}
			}
			trustStore := stubCertificateTrustStore(t)
			var events []string
			failure := errors.New("native trust command: permission denied")
			prepareCertificateTrustStoreFunc = func(io.Writer) (certificateTrustStore, error) {
				events = append(events, "prepare")
				if test.phase == "prepare" {
					return certificateTrustStore{}, failure
				}
				return *trustStore, nil
			}
			trustStore.remove = func(*x509.Certificate) (string, error) {
				events = append(events, "remove")
				if test.phase == "remove" {
					return "", failure
				}
				return "test", nil
			}
			trustStore.install = func(string) (string, error) {
				events = append(events, "install")
				return "", failure
			}
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			if code := Run(stdout, stderr, []string{"cert-install"}); code != 1 {
				t.Errorf("Run() code = %d, want 1", code)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), failure.Error()) || !strings.Contains(stderr.String(), caPath) {
				t.Errorf("failure output: stdout %q, stderr %q", stdout.String(), stderr.String())
			}
			wantEvents := "prepare"
			if test.phase == "remove" {
				wantEvents += ",remove"
			} else if test.phase == "install" {
				wantEvents += ",remove,install"
			}
			if strings.Join(events, ",") != wantEvents {
				t.Errorf("events = %q, want %q", events, wantEvents)
			}
			for path, original := range originals {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if test.phase != "install" && !bytes.Equal(data, original) {
					t.Errorf("cached file changed after %s failure: %s", test.phase, path)
				}
			}
			if !test.existing {
				if _, err := os.Stat(caPath); !os.IsNotExist(err) {
					t.Errorf("CA created after preflight failure: %v", err)
				}
			} else if test.phase == "install" {
				if _, err := readCertificateFile(caPath); err != nil {
					t.Errorf("generated CA unavailable for recovery: %v", err)
				}
				data, err := os.ReadFile(caPath)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Equal(data, originals[caPath]) {
					t.Error("late installation failure did not retain regenerated CA")
				}
			}
		})
	}
}

// TestGlobalTLSCertificateCoversLocalhostNames checks that the generated server
// certificate verifies against the local CA and covers the localhost defaults.
func TestGlobalTLSCertificateCoversLocalhostNames(t *testing.T) {
	cacheDir := t.TempDir()
	certificatePath, keyPath, err := ensureGlobalTLSCertificate(cacheDir, "site.localhost")
	if err != nil {
		t.Fatalf("ensureGlobalTLSCertificate() error = %v", err)
	}
	if certificatePath != filepath.Join(cacheDir, "polka", serveTLSSubdir, serveTLSCertFileName) {
		t.Fatalf("certificate path = %q, want global path", certificatePath)
	}
	if keyPath != filepath.Join(cacheDir, "polka", serveTLSSubdir, serveTLSKeyFileName) {
		t.Fatalf("key path = %q, want global path", keyPath)
	}
	assertEncryptedTLSKeyFile(t, keyPath)
	caCertificatePath, _ := globalTLSCACertificatePaths(cacheDir)

	data, err := os.ReadFile(certificatePath)
	if err != nil {
		t.Fatalf("ReadFile(cert) error = %v", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("pem.Decode(cert) = nil")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("x509.ParseCertificate() error = %v", err)
	}
	caData, err := os.ReadFile(caCertificatePath)
	if err != nil {
		t.Fatalf("ReadFile(ca cert) error = %v", err)
	}
	caBlock, _ := pem.Decode(caData)
	if caBlock == nil {
		t.Fatal("pem.Decode(ca cert) = nil")
	}
	caCertificate, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("x509.ParseCertificate(ca) error = %v", err)
	}
	if !caCertificate.IsCA {
		t.Fatal("ca certificate IsCA = false, want true")
	}
	if len(caCertificate.SubjectKeyId) == 0 || !bytes.Equal(certificate.AuthorityKeyId, caCertificate.SubjectKeyId) {
		t.Fatalf("server issuer key ID = %x, want CA subject key ID %x", certificate.AuthorityKeyId, caCertificate.SubjectKeyId)
	}
	if certificate.IsCA {
		t.Fatal("server certificate IsCA = true, want false")
	}
	if err := certificate.VerifyHostname("localhost"); err != nil {
		t.Fatalf("VerifyHostname(localhost) error = %v", err)
	}
	if err := certificate.VerifyHostname("site.localhost"); err != nil {
		t.Fatalf("VerifyHostname(site.localhost) error = %v", err)
	}
	if err := certificate.VerifyHostname("127.0.0.1"); err != nil {
		t.Fatalf("VerifyHostname(127.0.0.1) error = %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCertificate)
	if _, err := certificate.Verify(x509.VerifyOptions{DNSName: "site.localhost", Roots: roots}); err != nil {
		t.Fatalf("Verify(site.localhost with generated ca) error = %v", err)
	}
}

func TestEnsureGlobalTLSCertificateUsesExistingPlaintextCAKey(t *testing.T) {
	cacheDir := t.TempDir()
	if _, _, err := regenerateGlobalTLSCertificateWithOptions(cacheDir, tlsCAKeyStorageOptions{Policy: tlsCAKeyStoragePlaintextRequired}); err != nil {
		t.Fatalf("regenerateGlobalTLSCertificateWithOptions() error = %v", err)
	}
	caCertificatePath, caKeyPath := globalTLSCACertificatePaths(cacheDir)
	caBefore, err := readCertificateFile(caCertificatePath)
	if err != nil {
		t.Fatalf("readCertificateFile(ca before) error = %v", err)
	}
	caKeyData, err := os.ReadFile(caKeyPath)
	if err != nil {
		t.Fatalf("ReadFile(ca key) error = %v", err)
	}
	if !strings.Contains(string(caKeyData), "-----BEGIN RSA PRIVATE KEY-----") {
		t.Fatalf("ca key = %q, want plaintext key fixture", string(caKeyData))
	}

	certificatePath, _, err := ensureGlobalTLSCertificate(cacheDir, "plain.localhost")
	if err != nil {
		t.Fatalf("ensureGlobalTLSCertificate() error = %v", err)
	}
	caAfter, err := readCertificateFile(caCertificatePath)
	if err != nil {
		t.Fatalf("readCertificateFile(ca after) error = %v", err)
	}
	if caBefore.SerialNumber.Cmp(caAfter.SerialNumber) != 0 {
		t.Fatalf("ca serial changed from %s to %s, want existing plaintext CA reused", caBefore.SerialNumber, caAfter.SerialNumber)
	}
	certificate, err := readCertificateFile(certificatePath)
	if err != nil {
		t.Fatalf("readCertificateFile(server) error = %v", err)
	}
	if err := certificate.VerifyHostname("plain.localhost"); err != nil {
		t.Fatalf("VerifyHostname(plain.localhost) error = %v", err)
	}
}

func TestEnsureGlobalTLSCertificateRegeneratesServerCertificateForExactHost(t *testing.T) {
	cacheDir := t.TempDir()
	if _, _, err := regenerateGlobalTLSCertificate(cacheDir); err != nil {
		t.Fatalf("regenerateGlobalTLSCertificate() error = %v", err)
	}
	caCertificatePath, _ := globalTLSCACertificatePaths(cacheDir)
	caBefore, err := readCertificateFile(caCertificatePath)
	if err != nil {
		t.Fatalf("readCertificateFile(ca before) error = %v", err)
	}

	certificatePath, _, err := ensureGlobalTLSCertificate(cacheDir, "test-site.localhost")
	if err != nil {
		t.Fatalf("ensureGlobalTLSCertificate(test-site.localhost) error = %v", err)
	}
	caAfter, err := readCertificateFile(caCertificatePath)
	if err != nil {
		t.Fatalf("readCertificateFile(ca after) error = %v", err)
	}
	if caBefore.SerialNumber.Cmp(caAfter.SerialNumber) != 0 {
		t.Fatalf("ca serial changed from %s to %s, want serve to keep installed CA", caBefore.SerialNumber, caAfter.SerialNumber)
	}
	certificate, err := readCertificateFile(certificatePath)
	if err != nil {
		t.Fatalf("readCertificateFile(server) error = %v", err)
	}
	if err := certificate.VerifyHostname("test-site.localhost"); err != nil {
		t.Fatalf("VerifyHostname(test-site.localhost) error = %v", err)
	}
	if !certificateHasExactHost(certificate, "test-site.localhost") {
		t.Fatalf("server DNSNames = %#v, want exact test-site.localhost SAN", certificate.DNSNames)
	}
}

func TestGlobalTLSCertificateDirUsesDefaultCacheSibling(t *testing.T) {
	cacheDir := filepath.Join("cache-root", "polka", "cache")
	got := globalTLSCertificateDir(cacheDir)
	want := filepath.Join("cache-root", "polka", serveTLSSubdir)
	if got != want {
		t.Fatalf("globalTLSCertificateDir() = %q, want %q", got, want)
	}
}
