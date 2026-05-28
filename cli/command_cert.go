package cli

import (
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

var installCertificateToTrustStoreFunc = installCertificateToTrustStore

func newCertInstallCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "cert-install",
		Args: exactArgsError("cert-install accepts no arguments", 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			ctx.exitCode = runCertInstall(cmd.OutOrStdout(), cmd.ErrOrStderr(), store)
			return nil
		},
	}
	configureCommand(cmd, certInstallUsage)

	return cmd
}

func runCertInstall(stdout, stderr io.Writer, store backend.Store) int {
	if _, _, err := regenerateGlobalTLSCertificate(store.CacheDir); err != nil {
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

func runTrustStoreCommand(name string, args ...string) error {
	command := exec.Command(name, args...)
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}

	message := strings.TrimSpace(string(output))
	if message == "" {
		return fmt.Errorf("run %s: %w", name, err)
	}

	return fmt.Errorf("run %s: %w (%s)", name, err, message)
}
