package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"polka/config"
)

// frankenPHPPlugin installs the official server and exposes its bundled PHP CLI.
func frankenPHPPlugin() Plugin {
	return newManifestPlugin(FrankenPHP, pluginHooks{
		validate: func(environment config.Environment) error {
			version := strings.TrimSpace(environment.FrankenPHPVersion)
			if version == "" {
				return nil
			}

			return validateVersion(FrankenPHP, version)
		},
		postInstall: func(ctx InstallContext) error {
			if runtime.GOOS != "windows" {
				if err := os.Chmod(ctx.Result.TargetPath, 0o755); err != nil {
					return fmt.Errorf("make installed FrankenPHP executable: %w", err)
				}
				if err := writeFrankenPHPPHPCLIWrapper(filepath.Dir(ctx.Result.TargetPath)); err != nil {
					return err
				}
			}

			return configureInstalledFrankenPHP(ctx)
		},
	})
}

// writeFrankenPHPPHPCLIWrapper adapts the Linux subcommand into a normal php executable.
func writeFrankenPHPPHPCLIWrapper(installDir string) error {
	path := filepath.Join(installDir, PHP)
	contents := "#!/bin/sh\n" +
		"set -eu\n" +
		"SCRIPT_DIR=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" && pwd)\n" +
		"exec \"$SCRIPT_DIR/frankenphp\" php-cli \"$@\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		return fmt.Errorf("write FrankenPHP PHP CLI wrapper: %w", err)
	}

	return nil
}

// configureInstalledFrankenPHP applies PHP settings to the bundled CLI and server runtime.
func configureInstalledFrankenPHP(ctx InstallContext) error {
	installDir := filepath.Dir(ctx.Result.TargetPath)
	if _, err := ensureInstalledPHPOpenSSLConfigAt(installDir); err != nil {
		return err
	}

	phpConfig := EffectivePHPConfigForInstall(ctx.Environment)
	if phpConfigNeedsCABundle(phpConfig) {
		caBundlePath, err := ensureInstalledPHPCABundle(ctx.EnvsDir, ctx.Result.Tool, ctx.Result.Version)
		if err != nil {
			return err
		}
		phpConfig.CABundlePath = caBundlePath
	}
	if phpConfig.IsZero() {
		return nil
	}

	phpPath, err := resolveFrankenPHPPHPExecutable(installDir)
	if err != nil {
		return err
	}

	return configurePHPConfigAt(
		phpPath,
		filepath.Join(installDir, "ext"),
		filepath.Join(installDir, "php.ini"),
		phpConfig,
	)
}

// resolveFrankenPHPPHPExecutable finds the bundled CLI used to detect compiled-in modules.
func resolveFrankenPHPPHPExecutable(installDir string) (string, error) {
	for _, name := range []string{"php.exe", "php.cmd", "php.bat", "php"} {
		candidate := filepath.Join(installDir, name)
		info, err := os.Stat(candidate)
		switch {
		case err == nil && !info.IsDir():
			return candidate, nil
		case err == nil:
			continue
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			return "", fmt.Errorf("stat bundled FrankenPHP CLI %s: %w", candidate, err)
		}
	}

	return "", fmt.Errorf("frankenphp does not include a bundled PHP CLI in %s", installDir)
}
