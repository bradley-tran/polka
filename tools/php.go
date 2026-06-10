package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"polka/config"
)

const (
	phpWindowsReleaseURL = "https://windows.php.net/downloads/releases/releases.json"
	phpWindowsBaseURL    = "https://windows.php.net/downloads/releases"
)

type phpWindowsReleaseIndex map[string]phpWindowsRelease

type phpWindowsRelease struct {
	Version  string
	Variants map[string]phpWindowsVariant
}

type phpWindowsVariant struct {
	Zip phpWindowsAsset `json:"zip"`
}

type phpWindowsAsset struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func phpPlugin() Plugin {
	return newManifestPlugin(PHP, pluginHooks{
		validate: func(environment config.Environment) error {
			if version := strings.TrimSpace(environment.PHPVersion); version != "" {
				if err := validateVersion(PHP, version); err != nil {
					return err
				}
			}
			if err := validateOPcachePreset(environment.OPcachePreset); err != nil {
				return err
			}
			for name, value := range environment.OPcacheConfig {
				if err := validateOPcacheDirective(name, value); err != nil {
					return err
				}
			}

			return nil
		},
		download: func(ctx DownloadContext) error {
			return downloadPHP(ctx.Client, ctx.CacheDir, ctx.Version)
		},
		postInstall: func(ctx InstallContext) error {
			phpConfig := EffectivePHPConfigForInstall(ctx.Environment)
			if phpConfig.IsZero() {
				return nil
			}

			return configureInstalledPHPConfig(ctx.EnvsDir, ctx.Result.Version, phpConfig)
		},
	})
}

func validateOPcachePreset(preset string) error {
	switch config.NormalizeOPcachePreset(preset) {
	case "", config.OPcachePresetDev, config.OPcachePresetProduction:
		return nil
	default:
		return fmt.Errorf("unsupported opcache-preset %q: use none, dev, or production", preset)
	}
}

func downloadPHP(client *http.Client, cacheDir, version string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("automatic php download is only implemented on Windows")
	}
	if err := os.MkdirAll(filepath.Join(cacheDir, PHP), 0o755); err != nil {
		return fmt.Errorf("create php cache dir: %w", err)
	}

	index, err := fetchPHPWindowsReleaseIndex(client)
	if err != nil {
		return err
	}

	series := phpSeries(version)
	release, ok := index[series]
	if !ok {
		return fmt.Errorf("php version %q is not available in the Windows release index", version)
	}

	asset, err := selectPHPWindowsAsset(release)
	if err != nil {
		return err
	}

	cacheVersionDir := filepath.Join(cacheDir, PHP, version)
	stagingDir, err := os.MkdirTemp(filepath.Join(cacheDir, PHP), version+"-tmp-")
	if err != nil {
		return fmt.Errorf("create php staging dir: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	archivePath := filepath.Join(stagingDir, filepath.Base(asset.Path))
	archiveURL := fmt.Sprintf("%s/%s", phpWindowsBaseURL, asset.Path)
	if err := downloadFile(client, archiveURL, archivePath); err != nil {
		return err
	}
	if err := verifyChecksum(asset.SHA256, archivePath); err != nil {
		return err
	}
	if err := extractZipArchive(archivePath, stagingDir); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(stagingDir, "php.exe")); err != nil {
		return fmt.Errorf("downloaded php archive did not contain php.exe: %w", err)
	}

	return finalizeCacheVersion(cacheVersionDir, stagingDir)
}

func fetchPHPWindowsReleaseIndex(client *http.Client) (phpWindowsReleaseIndex, error) {
	response, err := client.Get(phpWindowsReleaseURL)
	if err != nil {
		return nil, fmt.Errorf("download php release index: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download php release index: unexpected status %s", response.Status)
	}

	var index phpWindowsReleaseIndex
	if err := json.NewDecoder(response.Body).Decode(&index); err != nil {
		return nil, fmt.Errorf("decode php release index: %w", err)
	}

	return index, nil
}

func (r *phpWindowsRelease) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	r.Variants = map[string]phpWindowsVariant{}
	for key, value := range raw {
		switch key {
		case "version":
			if err := json.Unmarshal(value, &r.Version); err != nil {
				return err
			}
		case "source", "test_pack":
			continue
		default:
			var variant phpWindowsVariant
			if err := json.Unmarshal(value, &variant); err != nil {
				continue
			}
			if variant.Zip.Path != "" {
				r.Variants[key] = variant
			}
		}
	}

	return nil
}

func selectPHPWindowsAsset(release phpWindowsRelease) (phpWindowsAsset, error) {
	architecture := "x64"
	if runtime.GOARCH == "386" {
		architecture = "x86"
	}

	preferences := []string{
		"nts-vs17-" + architecture,
		"nts-vs16-" + architecture,
		"nts-vc15-" + architecture,
		"ts-vs17-" + architecture,
		"ts-vs16-" + architecture,
		"ts-vc15-" + architecture,
	}

	for _, key := range preferences {
		if variant, ok := release.Variants[key]; ok && variant.Zip.Path != "" {
			return variant.Zip, nil
		}
	}

	return phpWindowsAsset{}, fmt.Errorf("no compatible Windows PHP binary found for %s on %s", release.Version, architecture)
}

func phpSeries(version string) string {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}

	return strings.TrimSpace(version)
}
