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

var postgreSQLVersionsURL = "https://www.postgresql.org/versions.json"

type postgreSQLVersionIndexEntry struct {
	Major       string `json:"major"`
	LatestMinor string `json:"latestMinor"`
	Supported   bool   `json:"supported"`
}

func postgreSQLPlugin() Plugin {
	return newManifestPlugin(PostgreSQL, pluginHooks{
		download: func(ctx DownloadContext) error {
			return downloadPostgreSQL(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	})
}

func downloadPostgreSQL(client *http.Client, cacheDir, version string) error {
	resolvedVersion, err := resolvePostgreSQLReleaseVersion(client, version)
	if err != nil {
		return err
	}
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		return downloadEmbeddedPostgreSQLLinux(client, cacheDir, version, resolvedVersion)
	}

	return downloadBuiltinManifestToolResolved(client, cacheDir, PostgreSQL, version, resolvedVersion, nil)
}

// downloadEmbeddedPostgreSQLLinux unwraps the portable PostgreSQL tarball
// distributed inside Zonky's Maven artifact and stores that tarball in cache.
func downloadEmbeddedPostgreSQLLinux(client *http.Client, cacheDir, requestedVersion, resolvedVersion string) error {
	manifest, err := loadBuiltinManifest(PostgreSQL)
	if err != nil {
		return err
	}
	mavenVersion := postgreSQLMavenVersion(resolvedVersion)
	asset, err := resolveManifestDownloadAsset(PostgreSQL, manifest.Download.Assets, requestedVersion, resolvedVersion, resolvedVersion, "linux", "amd64", map[string]string{
		"maven_version": mavenVersion,
	})
	if err != nil {
		return err
	}

	stagingDir, err := os.MkdirTemp("", "polka-postgresql-")
	if err != nil {
		return fmt.Errorf("create postgresql download staging directory: %w", err)
	}
	defer os.RemoveAll(stagingDir)
	jarPath := filepath.Join(stagingDir, asset.FileName)
	if err := downloadFile(client, asset.URL, jarPath); err != nil {
		return err
	}
	payloadPath, err := extractEmbeddedPostgreSQLPayload(jarPath, filepath.Join(stagingDir, "extracted"))
	if err != nil {
		return err
	}

	asset.FileName = "postgresql-" + resolvedVersion + "-linux-amd64.tar.xz"
	asset.ArchiveFormat = archiveFormatTarXz
	_, err = cacheArchivePayload(cacheDir, PostgreSQL, requestedVersion, resolvedVersion, asset, payloadPath)
	return err
}

func extractEmbeddedPostgreSQLPayload(jarPath, targetDir string) (string, error) {
	if err := extractZipArchive(jarPath, targetDir); err != nil {
		return "", fmt.Errorf("extract embedded postgresql archive: %w", err)
	}
	payloadPath := filepath.Join(targetDir, "postgres-linux-x86_64.txz")
	if _, err := os.Stat(payloadPath); err != nil {
		return "", fmt.Errorf("locate embedded postgresql payload: %w", err)
	}

	return payloadPath, nil
}

func postgreSQLMavenVersion(version string) string {
	if strings.Count(strings.TrimSpace(version), ".") == 1 {
		return strings.TrimSpace(version) + ".0"
	}

	return strings.TrimSpace(version)
}

// resolvePostgreSQLReleaseVersion resolves a major label such as 17 to the
// newest published minor release while accepting full major.minor labels.
func resolvePostgreSQLReleaseVersion(client *http.Client, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("postgresql version cannot be empty")
	}
	if strings.Contains(requested, ".") {
		return requested, nil
	}

	page, err := downloadText(client, postgreSQLVersionsURL, "postgresql versions index")
	if err != nil {
		return "", err
	}
	var entries []postgreSQLVersionIndexEntry
	if err := json.Unmarshal([]byte(page), &entries); err != nil {
		return "", fmt.Errorf("parse postgresql versions index: %w", err)
	}
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		major := strings.TrimSpace(entry.Major)
		minor := strings.TrimSpace(entry.LatestMinor)
		if !entry.Supported || major == "" || minor == "" {
			continue
		}
		versions = append(versions, major+"."+minor)
	}
	resolvedVersion, err := resolveMatchingVersion(versions, requested)
	if err != nil {
		return "", fmt.Errorf("resolve postgresql version %q: %w", requested, err)
	}

	return resolvedVersion, nil
}
