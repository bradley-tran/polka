package tools

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"polka/config"
)

func erlangPlugin() Plugin {
	return newManifestPlugin(Erlang, pluginHooks{download: func(ctx DownloadContext) error {
		if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
			return fmt.Errorf("erlang dependency for rabbitmq is only supported on windows/amd64")
		}
		return downloadRabbitMQGitHubAsset(ctx.Client, ctx.CacheDir, Erlang, ctx.Version)
	}})
}

func rabbitMQPlugin() Plugin {
	return newManifestPlugin(RabbitMQ, pluginHooks{
		validate: func(environment config.Environment) error {
			return validateRabbitMQConfig(environment.RabbitMQ)
		},
		download: func(ctx DownloadContext) error {
			if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
				return fmt.Errorf("rabbitmq is only supported on windows/amd64")
			}
			return downloadRabbitMQGitHubAsset(ctx.Client, ctx.CacheDir, RabbitMQ, ctx.Version)
		},
		dependencies: func(environment config.Environment) []InstallRequest {
			if environment.RabbitMQ == nil || strings.TrimSpace(environment.RabbitMQ.Version) == "" {
				return nil
			}
			return []InstallRequest{{Tool: Erlang, Version: "27"}}
		},
	})
}

// downloadRabbitMQGitHubAsset requires GitHub's published SHA-256 digest so
// RabbitMQ and Erlang are authenticated before entering Polka's cache.
func downloadRabbitMQGitHubAsset(client *http.Client, cacheDir, tool, requestedVersion string) error {
	manifest, err := loadBuiltinManifest(tool)
	if err != nil {
		return err
	}
	resolved, tag, githubAssets, err := resolveManifestDownloadVersion(client, tool, requestedVersion, manifest.Download)
	if err != nil {
		return err
	}
	if tool == RabbitMQ && (compareVersions(resolved, "4.0.4") < 0 || compareVersions(resolved, "5.0.0") >= 0) {
		return fmt.Errorf("rabbitmq version %q is unsupported: use 4.0.4 or newer in the 4.x series", resolved)
	}
	asset, err := resolveManifestDownloadAsset(tool, manifest.Download.Assets, requestedVersion, resolved, tag, runtime.GOOS, runtime.GOARCH, nil)
	if err != nil {
		return err
	}
	applyGitHubAssetDigest(&asset, githubAssets)
	if asset.ChecksumAlgorithm != checksumAlgorithmSHA256 || strings.TrimSpace(asset.Checksum) == "" {
		return fmt.Errorf("%s release asset %q does not publish a SHA-256 digest", tool, asset.SourceFileName)
	}
	return downloadManifestAsset(client, cacheDir, tool, requestedVersion, resolved, asset)
}

func validateRabbitMQConfig(rabbitMQ *config.RabbitMQConfig) error {
	if rabbitMQ == nil {
		return nil
	}
	if strings.TrimSpace(rabbitMQ.Version) == "" {
		return fmt.Errorf("rabbitmq configuration requires version")
	}
	if err := validateVersion(RabbitMQ, rabbitMQ.Version); err != nil {
		return err
	}
	if strings.Split(strings.TrimSpace(rabbitMQ.Version), ".")[0] != "4" {
		return fmt.Errorf("rabbitmq requires a 4.x version")
	}
	if rabbitMQ.Port != 0 && !validPort(rabbitMQ.Port) {
		return fmt.Errorf("rabbitmq port must be between 1 and 65535")
	}
	if rabbitMQ.ManagementPort != 0 && !validPort(rabbitMQ.ManagementPort) {
		return fmt.Errorf("rabbitmq management port must be between 1 and 65535")
	}
	port := rabbitMQ.Port
	if port == 0 {
		port = 5672
	}
	managementPort := rabbitMQ.ManagementPort
	if managementPort == 0 {
		managementPort = 15672
	}
	if port == managementPort {
		return fmt.Errorf("rabbitmq port and management port must be different")
	}
	if strings.ContainsAny(rabbitMQ.Username, "\r\n") || strings.ContainsAny(rabbitMQ.Password, "\r\n") {
		return fmt.Errorf("rabbitmq credentials must be single-line values")
	}

	return nil
}
