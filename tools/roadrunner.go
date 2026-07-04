package tools

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"polka/config"
)

// roadRunnerPlugin installs the RoadRunner PHP application server and dispatches
// its binary. RoadRunner is a managed installable tool only: users supply their
// own .rr.yaml and PSR worker script, so it is not selectable as a server.type.
func roadRunnerPlugin() Plugin {
	return newManifestPlugin(RoadRunner, pluginHooks{
		validate: func(environment config.Environment) error {
			version := strings.TrimSpace(environment.RoadRunnerVersion)
			if version == "" {
				return nil
			}
			return validateVersion(RoadRunner, version)
		},
		postInstall: chmodInstalledRoadRunner,
	})
}

// chmodInstalledRoadRunner marks the extracted RoadRunner binary executable on Unix.
func chmodInstalledRoadRunner(ctx InstallContext) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	target := ctx.Result.TargetPath
	if strings.TrimSpace(target) == "" {
		return nil
	}
	if err := os.Chmod(target, 0o755); err != nil {
		return fmt.Errorf("make roadrunner executable: %w", err)
	}

	return nil
}
