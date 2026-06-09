package tools

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var (
	mariaDBArchiveIndexURL       = "https://archive.mariadb.org/"
	mariaDBArchiveVersionPattern = regexp.MustCompile(`mariadb-([0-9]+\.[0-9]+\.[0-9]+)/`)
)

func mariaDBPlugin() Plugin {
	return newManifestPlugin(MariaDB, pluginHooks{
		download: func(ctx DownloadContext) error {
			return downloadMariaDB(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	})
}

func downloadMariaDB(client *http.Client, cacheDir, version string) error {
	resolvedVersion, err := resolveMariaDBReleaseVersion(client, version)
	if err != nil {
		return err
	}

	return downloadBuiltinManifestToolResolved(client, cacheDir, MariaDB, version, resolvedVersion, nil)
}

func resolveMariaDBReleaseVersion(client *http.Client, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("mariadb version cannot be empty")
	}
	if !versionNeedsResolution(requested) {
		return requested, nil
	}

	page, err := downloadText(client, mariaDBArchiveIndexURL, "mariadb archive index")
	if err != nil {
		return "", err
	}

	versions := []string{}
	seen := map[string]struct{}{}
	for _, match := range mariaDBArchiveVersionPattern.FindAllStringSubmatch(page, -1) {
		version := strings.TrimSpace(match[1])
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		versions = append(versions, version)
	}

	resolvedVersion, err := resolveMatchingVersion(versions, requested)
	if err != nil {
		return "", fmt.Errorf("resolve mariadb version %q: %w", requested, err)
	}

	return resolvedVersion, nil
}
