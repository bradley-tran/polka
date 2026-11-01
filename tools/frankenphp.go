package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

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
