package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var nodeJSReleaseIndexURL = "https://nodejs.org/download/release/index.json"

type nodeJSRelease struct {
	Version string `json:"version"`
}

func nodeJSPlugin() Plugin {
	return newManifestPlugin(NodeJS, pluginHooks{
		download: func(ctx DownloadContext) error {
			return downloadNodeJS(ctx.Client, ctx.CacheDir, ctx.Version)
		},
		postInstall: ensureNodeJSYarnShim,
	})
}

func downloadNodeJS(client *http.Client, cacheDir, version string) error {
	resolvedVersion, err := resolveNodeJSReleaseVersion(client, version)
	if err != nil {
		return err
	}

	return downloadBuiltinManifestToolResolvedWithTag(client, cacheDir, NodeJS, version, resolvedVersion, "v"+resolvedVersion, nil)
}

func resolveNodeJSDownloadAsset(client *http.Client, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	resolvedVersion, err := resolveNodeJSReleaseVersion(client, requestedVersion)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	manifest, err := loadBuiltinManifest(NodeJS)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	asset, err := resolveManifestDownloadAsset(NodeJS, manifest.Download.Assets, requestedVersion, resolvedVersion, "v"+resolvedVersion, goos, goarch, nil)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	return resolvedVersion, asset, nil
}

func resolveNodeJSReleaseVersion(client *http.Client, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("nodejs version cannot be empty")
	}
	if !versionNeedsResolution(requested) {
		return strings.TrimPrefix(requested, "v"), nil
	}

	text, err := downloadText(client, nodeJSReleaseIndexURL, "nodejs release index")
	if err != nil {
		return "", err
	}

	var releases []nodeJSRelease
	if err := json.Unmarshal([]byte(text), &releases); err != nil {
		return "", fmt.Errorf("decode nodejs release index: %w", err)
	}

	versions := make([]string, 0, len(releases))
	for _, release := range releases {
		version := strings.TrimPrefix(strings.TrimSpace(release.Version), "v")
		if version != "" {
			versions = append(versions, version)
		}
	}

	resolvedVersion, err := resolveMatchingVersion(versions, requested)
	if err != nil {
		return "", fmt.Errorf("resolve nodejs version %q: %w", requested, err)
	}

	return resolvedVersion, nil
}

// ensureNodeJSYarnShim adds a Yarn command backed by Corepack when the
// downloaded Node.js payload does not already ship a Yarn executable.
func ensureNodeJSYarnShim(ctx InstallContext) error {
	installDir := filepath.Join(ctx.EnvsDir, NodeJS, ctx.Result.Version)
	if strings.TrimSpace(ctx.Result.Version) == "" {
		return nil
	}
	if nodeJSCommandExists(installDir, Yarn) {
		return nil
	}

	corepackPath, ok, err := findNodeJSCommand(installDir, "corepack")
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	shimPath := filepath.Join(filepath.Dir(corepackPath), Yarn)
	if runtime.GOOS == "windows" {
		shimPath += ".cmd"
	}
	if err := os.MkdirAll(filepath.Dir(shimPath), 0o755); err != nil {
		return fmt.Errorf("create yarn shim directory: %w", err)
	}
	if err := os.WriteFile(shimPath, []byte(nodeJSYarnShimContents(runtime.GOOS)), 0o755); err != nil {
		return fmt.Errorf("write yarn shim %q: %w", shimPath, err)
	}

	return nil
}

func nodeJSCommandExists(installDir, command string) bool {
	_, ok, err := findNodeJSCommand(installDir, command)
	return err == nil && ok
}

func findNodeJSCommand(installDir, command string) (string, bool, error) {
	for _, candidate := range nodeJSCommandCandidates(installDir, command) {
		info, err := os.Stat(candidate)
		switch {
		case err == nil && !info.IsDir():
			return candidate, true, nil
		case err == nil:
			continue
		case os.IsNotExist(err):
			continue
		default:
			return "", false, fmt.Errorf("stat %s: %w", candidate, err)
		}
	}

	return "", false, nil
}

func nodeJSCommandCandidates(installDir, command string) []string {
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(installDir, command+".cmd"),
			filepath.Join(installDir, command),
			filepath.Join(installDir, "bin", command+".cmd"),
			filepath.Join(installDir, "bin", command),
		}
	}

	return []string{
		filepath.Join(installDir, "bin", command),
		filepath.Join(installDir, command),
	}
}

func nodeJSYarnShimContents(goos string) string {
	if goos == "windows" {
		return "@echo off\r\n" +
			"setlocal\r\n" +
			"set \"SCRIPT_DIR=%~dp0\"\r\n" +
			"set \"COREPACK=%SCRIPT_DIR%corepack.cmd\"\r\n" +
			"if not exist \"%COREPACK%\" set \"COREPACK=%SCRIPT_DIR%corepack\"\r\n" +
			"if not exist \"%COREPACK%\" (\r\n" +
			"  >&2 echo Corepack not found next to yarn shim.\r\n" +
			"  exit /b 1\r\n" +
			")\r\n" +
			"call \"%COREPACK%\" yarn %*\r\n" +
			"exit /b %ERRORLEVEL%\r\n"
	}

	return "#!/usr/bin/env sh\n" +
		"set -eu\n" +
		"SCRIPT_DIR=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" && pwd)\n" +
		"exec \"$SCRIPT_DIR/corepack\" yarn \"$@\"\n"
}
