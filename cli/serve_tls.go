package cli

// This file contains the local development TLS assets used by HTTPS serving:
// the Polka local CA, the shared server certificate covering the serve
// hostnames, and the helpers that provision runtime copies of the private key
// for webserver configs. Private key storage itself lives in tls_private_key.go.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	serveTLSSubdir       = "cert"
	serveTLSCACertName   = "polka-local-ca.crt"
	serveTLSCAKeyName    = "polka-local-ca.key"
	serveTLSCertFileName = "polka-local.crt"
	serveTLSKeyFileName  = "polka-local.key"
)

// serveTLSConfig carries the TLS material referenced by generated webserver configs.
type serveTLSConfig struct {
	Enabled            bool
	CertificatePath    string
	CertificateKeyPath string
}

func ensureGlobalTLSCertificate(cacheDir, host string) (string, string, error) {
	return ensureGlobalTLSCertificateWithOptions(cacheDir, host, defaultTLSCAKeyStorageOptions())
}

// ensureGlobalTLSCertificateRuntimeKey ensures the shared certificate covers
// the host and materializes a runtime copy of the private key next to the
// webserver runtime files.
func ensureGlobalTLSCertificateRuntimeKey(cacheDir, runtimeDir, host string) (string, string, error) {
	return ensureGlobalTLSCertificateRuntimeKeyWithOptions(cacheDir, runtimeDir, host, defaultTLSCAKeyStorageOptions())
}

func ensureGlobalTLSCertificateRuntimeKeyWithOptions(cacheDir, runtimeDir, host string, storageOptions tlsCAKeyStorageOptions) (string, string, error) {
	certPath, keyPath, err := ensureGlobalTLSCertificateWithOptions(cacheDir, host, storageOptions)
	if err != nil {
		return "", "", err
	}
	runtimeKeyPath := filepath.Join(runtimeDir, serveTLSKeyFileName)
	if err := materializeTLSPrivateKeyFile(keyPath, runtimeKeyPath, storageOptions); err != nil {
		return "", "", fmt.Errorf("write local tls runtime private key: %w", err)
	}

	return certPath, runtimeKeyPath, nil
}

// ensureGlobalTLSCertificateWithOptions returns the shared certificate and key
// paths, creating or re-issuing them when missing or when the certificate does
// not cover the requested host.
func ensureGlobalTLSCertificateWithOptions(cacheDir, host string, storageOptions tlsCAKeyStorageOptions) (string, string, error) {
	certPath, keyPath := globalTLSCertificatePaths(cacheDir)
	caCertPath, caKeyPath := globalTLSCACertificatePaths(cacheDir)
	hasCA := certificateFileExists(caCertPath) && certificateFileExists(caKeyPath)
	if certificateFileExists(certPath) && certificateFileExists(keyPath) && hasCA && serverCertificateCoversHost(certPath, host) {
		return certPath, keyPath, nil
	}
	if hasCA {
		return createGlobalTLSServerCertificateWithOptions(cacheDir, host, storageOptions)
	}

	return createGlobalTLSCertificateWithOptions(cacheDir, host, storageOptions)
}

// regenerateGlobalTLSCertificate removes and recreates the local CA and server certificate.
func regenerateGlobalTLSCertificate(cacheDir string) (string, string, error) {
	return regenerateGlobalTLSCertificateWithOptions(cacheDir, defaultTLSCAKeyStorageOptions())
}

func regenerateGlobalTLSCertificateWithOptions(cacheDir string, storageOptions tlsCAKeyStorageOptions) (string, string, error) {
	certPath, keyPath := globalTLSCertificatePaths(cacheDir)
	caCertPath, caKeyPath := globalTLSCACertificatePaths(cacheDir)
	for _, path := range []string{certPath, keyPath, caCertPath, caKeyPath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("remove existing tls certificate file %s: %w", path, err)
		}
	}

	return createGlobalTLSCertificateWithOptions(cacheDir, "", storageOptions)
}

func createGlobalTLSCertificate(cacheDir, host string) (string, string, error) {
	return createGlobalTLSCertificateWithOptions(cacheDir, host, defaultTLSCAKeyStorageOptions())
}

// createGlobalTLSCertificateWithOptions creates a fresh local CA and issues a
// server certificate covering the host.
func createGlobalTLSCertificateWithOptions(cacheDir, host string, storageOptions tlsCAKeyStorageOptions) (string, string, error) {
	certPath, _ := globalTLSCertificatePaths(cacheDir)
	caCertPath, caKeyPath := globalTLSCACertificatePaths(cacheDir)
	certDir := filepath.Dir(certPath)
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create local tls certificate directory: %w", err)
	}

	caPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", fmt.Errorf("generate local ca private key: %w", err)
	}
	caSerialNumber, err := randomTLSSerialNumber()
	if err != nil {
		return "", "", err
	}

	now := serveNowFunc().UTC()
	caTemplate := x509.Certificate{
		SerialNumber: caSerialNumber,
		Subject: pkix.Name{
			CommonName: "Polka Local Development CA",
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caPrivateKey.PublicKey, caPrivateKey)
	if err != nil {
		return "", "", fmt.Errorf("generate local ca certificate: %w", err)
	}

	if err := writePEMFile(caCertPath, 0o644, "CERTIFICATE", caDER); err != nil {
		return "", "", fmt.Errorf("write local ca certificate: %w", err)
	}
	if err := writeTLSPrivateKeyFile(caKeyPath, caPrivateKey, storageOptions); err != nil {
		return "", "", fmt.Errorf("write local ca private key: %w", err)
	}

	return createGlobalTLSServerCertificateWithCA(cacheDir, host, &caTemplate, caPrivateKey, storageOptions)
}

func createGlobalTLSServerCertificate(cacheDir, host string) (string, string, error) {
	return createGlobalTLSServerCertificateWithOptions(cacheDir, host, defaultTLSCAKeyStorageOptions())
}

// createGlobalTLSServerCertificateWithOptions issues a server certificate
// signed by the existing local CA.
func createGlobalTLSServerCertificateWithOptions(cacheDir, host string, storageOptions tlsCAKeyStorageOptions) (string, string, error) {
	caCertificate, caPrivateKey, err := loadGlobalTLSCAWithOptions(cacheDir, storageOptions)
	if err != nil {
		return "", "", err
	}

	return createGlobalTLSServerCertificateWithCA(cacheDir, host, caCertificate, caPrivateKey, storageOptions)
}

// createGlobalTLSServerCertificateWithCA issues and stores the shared server
// certificate for the host using the provided CA material.
func createGlobalTLSServerCertificateWithCA(cacheDir, host string, caCertificate *x509.Certificate, caPrivateKey *rsa.PrivateKey, storageOptions tlsCAKeyStorageOptions) (string, string, error) {
	certPath, keyPath := globalTLSCertificatePaths(cacheDir)
	serverPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", fmt.Errorf("generate local tls private key: %w", err)
	}
	serverSerialNumber, err := randomTLSSerialNumber()
	if err != nil {
		return "", "", err
	}
	dnsNames, ipAddresses := serverCertificateNames(host)
	now := serveNowFunc().UTC()
	serverTemplate := x509.Certificate{
		SerialNumber: serverSerialNumber,
		Subject: pkix.Name{
			CommonName: firstServerCertificateCommonName(dnsNames, ipAddresses),
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.AddDate(2, 0, 0),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, &serverTemplate, caCertificate, &serverPrivateKey.PublicKey, caPrivateKey)
	if err != nil {
		return "", "", fmt.Errorf("generate local tls certificate: %w", err)
	}

	if err := writePEMFile(certPath, 0o644, "CERTIFICATE", serverDER); err != nil {
		return "", "", fmt.Errorf("write local tls certificate: %w", err)
	}
	if err := writeTLSPrivateKeyFile(keyPath, serverPrivateKey, storageOptions); err != nil {
		return "", "", fmt.Errorf("write local tls private key: %w", err)
	}

	return certPath, keyPath, nil
}

func loadGlobalTLSCA(cacheDir string) (*x509.Certificate, *rsa.PrivateKey, error) {
	return loadGlobalTLSCAWithOptions(cacheDir, defaultTLSCAKeyStorageOptions())
}

// loadGlobalTLSCAWithOptions reads the local CA certificate and private key.
func loadGlobalTLSCAWithOptions(cacheDir string, storageOptions tlsCAKeyStorageOptions) (*x509.Certificate, *rsa.PrivateKey, error) {
	caCertPath, caKeyPath := globalTLSCACertificatePaths(cacheDir)
	caCertificate, err := readCertificateFile(caCertPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read local ca certificate: %w", err)
	}
	caPrivateKey, err := readTLSPrivateKeyFile(caKeyPath, storageOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("read local ca private key: %w", err)
	}

	return caCertificate, caPrivateKey, nil
}

// serverCertificateCoversHost reports whether the stored server certificate
// explicitly lists the host among its names.
func serverCertificateCoversHost(certificatePath, host string) bool {
	trimmed := strings.Trim(strings.TrimSpace(host), "[]")
	if trimmed == "" {
		return true
	}
	certificate, err := readCertificateFile(certificatePath)
	if err != nil {
		return false
	}

	return certificate.VerifyHostname(trimmed) == nil && certificateHasExactHost(certificate, trimmed)
}

// certificateHasExactHost reports whether the certificate lists the host as an
// exact DNS name or IP address (wildcard matches are not enough).
func certificateHasExactHost(certificate *x509.Certificate, host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		for _, candidate := range certificate.IPAddresses {
			if candidate.Equal(ip) {
				return true
			}
		}

		return false
	}
	for _, candidate := range certificate.DNSNames {
		if strings.EqualFold(candidate, host) {
			return true
		}
	}

	return false
}

// serverCertificateNames returns the DNS names and IP addresses the server
// certificate should cover: the localhost defaults plus the requested host.
func serverCertificateNames(host string) ([]string, []net.IP) {
	dnsNames := []string{"localhost", "*.localhost"}
	ipAddresses := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	trimmed := strings.Trim(strings.TrimSpace(host), "[]")
	if trimmed == "" {
		return dnsNames, ipAddresses
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		for _, existing := range ipAddresses {
			if existing.Equal(ip) {
				return dnsNames, ipAddresses
			}
		}

		return dnsNames, append(ipAddresses, ip)
	}
	for _, existing := range dnsNames {
		if strings.EqualFold(existing, trimmed) {
			return dnsNames, ipAddresses
		}
	}

	return append(dnsNames, trimmed), ipAddresses
}

// firstServerCertificateCommonName picks a subject common name from the
// certificate names, skipping wildcard entries.
func firstServerCertificateCommonName(dnsNames []string, ipAddresses []net.IP) string {
	for _, name := range dnsNames {
		if name != "" && !strings.HasPrefix(name, "*.") {
			return name
		}
	}
	if len(ipAddresses) > 0 {
		return ipAddresses[0].String()
	}

	return "localhost"
}

// randomTLSSerialNumber generates a random 128-bit certificate serial number.
func randomTLSSerialNumber() (*big.Int, error) {
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("generate tls serial number: %w", err)
	}

	return serialNumber, nil
}

// writePEMFile writes one PEM block of the given type to a file.
func writePEMFile(path string, mode os.FileMode, blockType string, data []byte) error {
	pemData := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: data})

	return os.WriteFile(path, pemData, mode)
}

// readCertificateFile reads and parses a PEM-encoded certificate.
func readCertificateFile(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("missing CERTIFICATE PEM block in %s", path)
	}

	return x509.ParseCertificate(block.Bytes)
}

// globalTLSCertificatePaths returns the shared server certificate and key paths.
func globalTLSCertificatePaths(cacheDir string) (string, string) {
	certDir := globalTLSCertificateDir(cacheDir)
	certPath := filepath.Join(certDir, serveTLSCertFileName)
	keyPath := filepath.Join(certDir, serveTLSKeyFileName)

	return certPath, keyPath
}

// globalTLSCACertificatePaths returns the local CA certificate and key paths.
func globalTLSCACertificatePaths(cacheDir string) (string, string) {
	certDir := globalTLSCertificateDir(cacheDir)
	certPath := filepath.Join(certDir, serveTLSCACertName)
	keyPath := filepath.Join(certDir, serveTLSCAKeyName)

	return certPath, keyPath
}

// globalTLSCertificateDir returns the machine-global certificate directory,
// which lives next to (not inside) the polka cache directory.
func globalTLSCertificateDir(cacheDir string) string {
	cleanCacheDir := filepath.Clean(cacheDir)
	base := filepath.Base(cleanCacheDir)
	if strings.EqualFold(base, "cache") && strings.EqualFold(filepath.Base(filepath.Dir(cleanCacheDir)), "polka") {
		return filepath.Join(filepath.Dir(cleanCacheDir), serveTLSSubdir)
	}

	return filepath.Join(cleanCacheDir, "polka", serveTLSSubdir)
}

// certificateFileExists reports whether the path exists as a regular file.
func certificateFileExists(path string) bool {
	fileInfo, err := os.Stat(path)
	return err == nil && !fileInfo.IsDir()
}
