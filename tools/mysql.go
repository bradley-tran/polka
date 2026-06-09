package tools

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var (
	mysqlDownloadPageURLPattern = "https://dev.mysql.com/downloads/mysql/%s.html"
	mysqlDownloadVersionPattern = regexp.MustCompile(`(?:mysql-|MySQL Community Server )([0-9]+\.[0-9]+\.[0-9]+)`)
)

func mysqlPlugin() Plugin {
	return newManifestPlugin(MySQL, pluginHooks{
		download: func(ctx DownloadContext) error {
			return downloadMySQL(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	})
}

func downloadMySQL(client *http.Client, cacheDir, version string) error {
	resolvedVersion, err := resolveMySQLReleaseVersion(client, version)
	if err != nil {
		return err
	}

	return downloadBuiltinManifestToolResolved(client, cacheDir, MySQL, version, resolvedVersion, map[string]string{
		"major_minor": versionMajorMinor(resolvedVersion),
	})
}

func resolveMySQLReleaseVersion(client *http.Client, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("mysql version cannot be empty")
	}
	if !versionNeedsResolution(requested) {
		return requested, nil
	}

	pageURL := fmt.Sprintf(mysqlDownloadPageURLPattern, requested)
	page, err := downloadText(client, pageURL, "mysql download page")
	if err != nil {
		return "", err
	}

	seen := map[string]struct{}{}
	versions := []string{}
	for _, match := range mysqlDownloadVersionPattern.FindAllStringSubmatch(page, -1) {
		version := strings.TrimSpace(match[1])
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		versions = append(versions, version)
	}

	resolvedVersion, err := resolveMatchingVersion(versions, requested)
	if err != nil {
		return "", fmt.Errorf("resolve mysql version %q: %w", requested, err)
	}

	return resolvedVersion, nil
}
