package cli

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

const (
	tlsPrivateKeyringService        = "polka"
	tlsPrivateKeyringAccountPrefix  = "local-https-rsa-key:"
	tlsLegacyCAKeyringAccountPrefix = "local-https-ca:"
	tlsEncryptedPrivateKeyPEMType   = "POLKA ENCRYPTED RSA PRIVATE KEY"
	tlsLegacyEncryptedCAKeyPEMType  = "POLKA ENCRYPTED CA PRIVATE KEY"
	tlsPrivateKeyVersion            = 1
	tlsPrivateKeyAlgorithm          = "AES-256-GCM"
	tlsPrivateKeyEncryptionKeySize  = 32
	tlsCAEncryptedKeyPEMType        = tlsEncryptedPrivateKeyPEMType
)

// tlsCAKeyStoragePolicy selects how Polka stores generated TLS private keys.
type tlsCAKeyStoragePolicy int

const (
	tlsCAKeyStorageFlexible tlsCAKeyStoragePolicy = iota
	tlsCAKeyStorageEncryptedRequired
	tlsCAKeyStoragePlaintextRequired
)

// tlsCAKeyStorageOptions controls whether TLS private key writes require
// keyring-backed encryption, require plaintext, or may choose either.
type tlsCAKeyStorageOptions struct {
	Policy tlsCAKeyStoragePolicy
	Warnf  func(format string, args ...any)
}

// encryptedTLSCAKeyFile is the JSON payload stored inside the encrypted key
// PEM block.
type encryptedTLSCAKeyFile struct {
	Version    int    `json:"version"`
	Algorithm  string `json:"algorithm"`
	KeyID      string `json:"key_id"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

var tlsCAKeyFallbackWarnf func(format string, args ...any)

// setTLSCAKeyFallbackWarningWriter installs a temporary warning sink for the
// flexible serve path, where plaintext fallback is allowed but should be
// visible to the user.
func setTLSCAKeyFallbackWarningWriter(stderr io.Writer) func() {
	oldWarnf := tlsCAKeyFallbackWarnf
	if stderr == nil {
		tlsCAKeyFallbackWarnf = nil
	} else {
		tlsCAKeyFallbackWarnf = func(format string, args ...any) {
			_, _ = fmt.Fprintf(stderr, "warning: "+format, args...)
		}
	}

	return func() {
		tlsCAKeyFallbackWarnf = oldWarnf
	}
}

// defaultTLSCAKeyStorageOptions returns the flexible storage behavior used by
// day-to-day HTTPS serving.
func defaultTLSCAKeyStorageOptions() tlsCAKeyStorageOptions {
	return tlsCAKeyStorageOptions{
		Policy: tlsCAKeyStorageFlexible,
		Warnf:  tlsCAKeyFallbackWarnf,
	}
}

func (options tlsCAKeyStorageOptions) warnf(format string, args ...any) {
	if options.Warnf != nil {
		options.Warnf(format, args...)
	}
}

// writeTLSPrivateKeyFile writes a TLS key according to the selected storage policy.
func writeTLSPrivateKeyFile(path string, privateKey *rsa.PrivateKey, options tlsCAKeyStorageOptions) error {
	keyDER := x509.MarshalPKCS1PrivateKey(privateKey)
	switch options.Policy {
	case tlsCAKeyStoragePlaintextRequired:
		return writePEMFile(path, 0o600, "RSA PRIVATE KEY", keyDER)
	case tlsCAKeyStorageEncryptedRequired:
		return writeEncryptedTLSPrivateKeyFile(path, keyDER)
	default:
		if err := writeEncryptedTLSPrivateKeyFile(path, keyDER); err != nil {
			options.warnf("OS keyring is unavailable; writing plaintext Polka local HTTPS private key: %v\n", err)
			return writePEMFile(path, 0o600, "RSA PRIVATE KEY", keyDER)
		}

		return nil
	}
}

// readTLSPrivateKeyFile reads either encrypted Polka TLS keys or the legacy
// plaintext RSA private key format.
func readTLSPrivateKeyFile(path string, options tlsCAKeyStorageOptions) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("missing PEM block in %s", path)
	}

	switch block.Type {
	case "RSA PRIVATE KEY":
		if options.Policy == tlsCAKeyStorageEncryptedRequired {
			return nil, fmt.Errorf("local HTTPS private key %s is plaintext; regenerate it with encrypted storage", path)
		}

		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case tlsEncryptedPrivateKeyPEMType:
		return decryptTLSPrivateKey(path, block.Bytes, tlsKeyringAccount)
	case tlsLegacyEncryptedCAKeyPEMType:
		return decryptTLSPrivateKey(path, block.Bytes, tlsLegacyCAKeyringAccount)
	default:
		return nil, fmt.Errorf("missing RSA PRIVATE KEY or %s PEM block in %s", tlsEncryptedPrivateKeyPEMType, path)
	}
}

func writeTLSCAPrivateKeyFile(path string, privateKey *rsa.PrivateKey, options tlsCAKeyStorageOptions) error {
	return writeTLSPrivateKeyFile(path, privateKey, options)
}

func readTLSCAPrivateKeyFile(path string, options tlsCAKeyStorageOptions) (*rsa.PrivateKey, error) {
	return readTLSPrivateKeyFile(path, options)
}

// materializeTLSPrivateKeyFile writes a plaintext runtime copy for servers that
// require a filesystem PEM key.
func materializeTLSPrivateKeyFile(sourcePath, targetPath string, options tlsCAKeyStorageOptions) error {
	privateKey, err := readTLSPrivateKeyFile(sourcePath, options)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create local HTTPS private key runtime directory: %w", err)
	}

	return writePEMFile(targetPath, 0o600, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(privateKey))
}

// writeEncryptedTLSPrivateKeyFile encrypts the DER-encoded private key
// with AES-GCM and stores the AES key in the OS keyring.
func writeEncryptedTLSPrivateKeyFile(path string, keyDER []byte) error {
	keyID, err := tlsKeyringAccount(path)
	if err != nil {
		return err
	}
	encryptionKey, err := ensureTLSPrivateKeyEncryptionKey(keyID)
	if err != nil {
		return fmt.Errorf("store local HTTPS private key encryption key in OS keyring: %w", err)
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return fmt.Errorf("initialize local HTTPS private key encryption: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("initialize local HTTPS private key encryption mode: %w", err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate local HTTPS private key encryption nonce: %w", err)
	}
	payload := encryptedTLSCAKeyFile{
		Version:   tlsPrivateKeyVersion,
		Algorithm: tlsPrivateKeyAlgorithm,
		KeyID:     keyID,
		Nonce:     base64.StdEncoding.EncodeToString(nonce),
	}
	ciphertext := aead.Seal(nil, nonce, keyDER, []byte(keyID))
	payload.Ciphertext = base64.StdEncoding.EncodeToString(ciphertext)
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode encrypted local HTTPS private key metadata: %w", err)
	}

	pemData := pem.EncodeToMemory(&pem.Block{Type: tlsEncryptedPrivateKeyPEMType, Bytes: data})
	return os.WriteFile(path, pemData, 0o600)
}

// decryptTLSPrivateKey decrypts and parses an encrypted Polka TLS key PEM
// payload.
func decryptTLSPrivateKey(path string, data []byte, accountFunc func(string) (string, error)) (*rsa.PrivateKey, error) {
	var payload encryptedTLSCAKeyFile
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode encrypted local HTTPS private key metadata: %w", err)
	}
	if payload.Version != tlsPrivateKeyVersion {
		return nil, fmt.Errorf("unsupported encrypted local HTTPS private key version %d", payload.Version)
	}
	if payload.Algorithm != tlsPrivateKeyAlgorithm {
		return nil, fmt.Errorf("unsupported encrypted local HTTPS private key algorithm %q", payload.Algorithm)
	}
	keyID, err := accountFunc(path)
	if err != nil {
		return nil, err
	}
	if payload.KeyID != keyID {
		return nil, fmt.Errorf("encrypted local HTTPS private key belongs to %q, want %q", payload.KeyID, keyID)
	}
	nonce, err := base64.StdEncoding.DecodeString(payload.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode encrypted local HTTPS private key nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(payload.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode encrypted local HTTPS private key ciphertext: %w", err)
	}
	encryptionKey, err := loadTLSPrivateKeyEncryptionKey(keyID)
	if err != nil {
		return nil, fmt.Errorf("read local HTTPS private key encryption key from OS keyring: %w", err)
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("initialize local HTTPS private key decryption: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize local HTTPS private key decryption mode: %w", err)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte(keyID))
	if err != nil {
		return nil, fmt.Errorf("decrypt local HTTPS private key: %w", err)
	}

	return x509.ParsePKCS1PrivateKey(plaintext)
}

// ensureTLSPrivateKeyEncryptionKey loads the AES key from the OS keyring, creating it
// on first use.
func ensureTLSPrivateKeyEncryptionKey(keyID string) ([]byte, error) {
	key, err := loadTLSPrivateKeyEncryptionKey(keyID)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return nil, err
	}

	key = make([]byte, tlsPrivateKeyEncryptionKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate local HTTPS private key encryption key: %w", err)
	}
	encodedKey := base64.StdEncoding.EncodeToString(key)
	if err := keyring.Set(tlsPrivateKeyringService, keyID, encodedKey); err != nil {
		return nil, err
	}

	return key, nil
}

// loadTLSPrivateKeyEncryptionKey reads and validates a keyring-stored AES key.
func loadTLSPrivateKeyEncryptionKey(keyID string) ([]byte, error) {
	encodedKey, err := keyring.Get(tlsPrivateKeyringService, keyID)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		return nil, fmt.Errorf("decode local HTTPS private key encryption key: %w", err)
	}
	if len(key) != tlsPrivateKeyEncryptionKeySize {
		return nil, fmt.Errorf("local HTTPS private key encryption key has %d bytes, want %d", len(key), tlsPrivateKeyEncryptionKeySize)
	}

	return key, nil
}

// tlsKeyringAccount derives a stable keyring account name from the absolute key path.
func tlsKeyringAccount(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve local HTTPS private key path: %w", err)
	}
	hash := sha256.Sum256([]byte(filepath.Clean(absolutePath)))

	return tlsPrivateKeyringAccountPrefix + hex.EncodeToString(hash[:]), nil
}

func tlsLegacyCAKeyringAccount(path string) (string, error) {
	certDir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return "", fmt.Errorf("resolve local HTTPS private key directory: %w", err)
	}
	hash := sha256.Sum256([]byte(filepath.Clean(certDir)))

	return tlsLegacyCAKeyringAccountPrefix + hex.EncodeToString(hash[:]), nil
}
