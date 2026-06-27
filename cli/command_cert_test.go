package cli

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCertInstallCreatesAndInstallsGlobalCertificate(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	oldInstall := installCertificateToTrustStoreFunc
	t.Cleanup(func() {
		installCertificateToTrustStoreFunc = oldInstall
	})

	installCalls := 0
	installedPath := ""
	installCertificateToTrustStoreFunc = func(certificatePath string) (string, error) {
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

	oldInstall := installCertificateToTrustStoreFunc
	t.Cleanup(func() {
		installCertificateToTrustStoreFunc = oldInstall
	})
	installCertificateToTrustStoreFunc = func(certificatePath string) (string, error) {
		return "test", nil
	}

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

	oldInstall := installCertificateToTrustStoreFunc
	oldRemove := removeCertificateFromTrustStoreFunc
	t.Cleanup(func() {
		installCertificateToTrustStoreFunc = oldInstall
		removeCertificateFromTrustStoreFunc = oldRemove
	})

	events := []string{}
	removeCertificateFromTrustStoreFunc = func(certificate *x509.Certificate) (string, error) {
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
	installCertificateToTrustStoreFunc = func(certificatePath string) (string, error) {
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
	if got := strings.Join(events, ","); got != "remove,install" {
		t.Fatalf("trust-store events = %q, want remove,install", got)
	}
}

func TestRunCertInstallNoEncryptionCreatesPlaintextCAKey(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	oldInstall := installCertificateToTrustStoreFunc
	t.Cleanup(func() {
		installCertificateToTrustStoreFunc = oldInstall
	})
	installCertificateToTrustStoreFunc = func(certificatePath string) (string, error) {
		return "test", nil
	}

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
