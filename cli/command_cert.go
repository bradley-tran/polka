package cli

import (
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"polka/backend"
)

var (
	installCertificateToTrustStoreFunc  = installCertificateToTrustStore
	removeCertificateFromTrustStoreFunc = removeCertificateFromTrustStore
)

// certInstallInput carries certificate regeneration options parsed from flags.
type certInstallInput struct {
	NoEncryption bool
}

func newCertInstallCommand(ctx *commandContext) *cobra.Command {
	var input certInstallInput

	cmd := &cobra.Command{
		Use:  "cert-install",
		Args: exactArgsError("cert-install accepts no arguments", 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			ctx.exitCode = runCertInstall(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, input)
			return nil
		},
	}
	cmd.Flags().BoolVar(&input.NoEncryption, "no-encryption", false, "write plaintext local HTTPS private keys instead of encrypting them with the OS keyring")
	configureCommand(cmd, certInstallUsage)

	return cmd
}

func runCertInstall(stdout, stderr io.Writer, store backend.Store, input certInstallInput) int {
	storageOptions := tlsCAKeyStorageOptions{Policy: tlsCAKeyStorageEncryptedRequired}
	if input.NoEncryption {
		storageOptions.Policy = tlsCAKeyStoragePlaintextRequired
		fmt.Fprintln(stderr, "warning: writing Polka local HTTPS private keys without OS keyring encryption")
	}

	if oldCACertificate := readExistingGlobalTLSCACertificate(store.CacheDir); oldCACertificate != nil {
		if _, err := removeCertificateFromTrustStoreFunc(oldCACertificate); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}

	if _, _, err := regenerateGlobalTLSCertificateWithOptions(store.CacheDir, storageOptions); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	certificatePath, _ := globalTLSCACertificatePaths(store.CacheDir)
	trustStore, err := installCertificateToTrustStoreFunc(certificatePath)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Installed Polka local HTTPS CA certificate into the %s trust store.\n", trustStore)
	fmt.Fprintf(stdout, "CA certificate: %s\n", certificatePath)
	return 0
}

// readExistingGlobalTLSCACertificate loads the cached CA certificate when it
// can be used to identify an existing trust-store entry.
func readExistingGlobalTLSCACertificate(cacheDir string) *x509.Certificate {
	certificatePath, _ := globalTLSCACertificatePaths(cacheDir)
	certificate, err := readCertificateFile(certificatePath)
	if err == nil {
		return certificate
	}
	if os.IsNotExist(err) {
		return nil
	}

	// Malformed legacy files cannot identify a trust-store entry, but they
	// should not prevent cert-install from regenerating fresh certificate files.
	return nil
}

func installCertificateToTrustStore(certificatePath string) (string, error) {
	absolutePath, err := filepath.Abs(certificatePath)
	if err != nil {
		return "", fmt.Errorf("resolve certificate path: %w", err)
	}

	switch runtime.GOOS {
	case "windows":
		if err := runTrustStoreCommand("certutil", "-user", "-addstore", "Root", absolutePath); err != nil {
			return "", err
		}

		return "current user Root", nil
	case "darwin":
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		keychainPath := filepath.Join(homeDir, "Library", "Keychains", "login.keychain-db")
		if err := runTrustStoreCommand("security", "add-trusted-cert", "-r", "trustRoot", "-k", keychainPath, absolutePath); err != nil {
			return "", err
		}

		return "login keychain", nil
	default:
		return "", fmt.Errorf("automatic trust-store installation is not supported on %s; install %s manually", runtime.GOOS, absolutePath)
	}
}

// removeCertificateFromTrustStore removes an exact CA certificate match from
// the current user's trust store when automatic trust-store management exists.
func removeCertificateFromTrustStore(certificate *x509.Certificate) (string, error) {
	if certificate == nil {
		return "", fmt.Errorf("remove certificate from trust store: missing certificate")
	}

	thumbprint := certificateSHA1Thumbprint(certificate)
	switch runtime.GOOS {
	case "windows":
		if err := runTrustStoreCommandAllowMissing("certutil", "-user", "-delstore", "Root", thumbprint); err != nil {
			return "", err
		}

		return "current user Root", nil
	case "darwin":
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		keychainPath := filepath.Join(homeDir, "Library", "Keychains", "login.keychain-db")
		if err := runTrustStoreCommandAllowMissing("security", "delete-certificate", "-Z", thumbprint, keychainPath); err != nil {
			return "", err
		}

		return "login keychain", nil
	default:
		// Keep unsupported platforms on the install path so they still receive
		// the regenerated certificate location for manual trust-store setup.
		return "", nil
	}
}

// certificateSHA1Thumbprint returns the certificate fingerprint format used by
// Windows certutil and macOS security trust-store deletion commands.
func certificateSHA1Thumbprint(certificate *x509.Certificate) string {
	sum := sha1.Sum(certificate.Raw)

	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func runTrustStoreCommand(name string, args ...string) error {
	command := exec.Command(name, args...)
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}

	message := strings.TrimSpace(string(output))
	return formatTrustStoreCommandError(name, err, message)
}

// runTrustStoreCommandAllowMissing treats "certificate not found" as success
// so stale cache files from untrusted CAs can still be regenerated.
func runTrustStoreCommandAllowMissing(name string, args ...string) error {
	command := exec.Command(name, args...)
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}

	message := strings.TrimSpace(string(output))
	if trustStoreCertificateMissing(message) {
		return nil
	}

	return formatTrustStoreCommandError(name, err, message)
}

func formatTrustStoreCommandError(name string, err error, message string) error {
	if message == "" {
		return fmt.Errorf("run %s: %w", name, err)
	}

	return fmt.Errorf("run %s: %w (%s)", name, err, message)
}

// trustStoreCertificateMissing recognizes platform command output for a
// certificate thumbprint that is already absent from the trust store.
func trustStoreCertificateMissing(message string) bool {
	normalized := strings.ToLower(message)
	for _, fragment := range []string{
		"0x80092004",
		"crypt_e_not_found",
		"cannot find object or property",
		"cannot find the requested object",
		"the specified item could not be found",
		"could not be found in the keychain",
		"unable to find certificate",
	} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}

	return false
}
