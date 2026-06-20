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

// frankenPHPPlugin installs the official server binary without treating it as the PHP CLI provider.
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

				return nil
			}

			return configureInstalledWindowsFrankenPHP(ctx)
		},
	})
}

// configureInstalledWindowsFrankenPHP enables the dynamic extensions shipped in the Windows archive.
func configureInstalledWindowsFrankenPHP(ctx InstallContext) error {
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

	installDir := filepath.Dir(ctx.Result.TargetPath)
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
