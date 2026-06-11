package tools

import (
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

var sqliteDownloadPageURL = "https://www.sqlite.org/download.html"

type sqliteDownloadProduct struct {
	Version     string
	RelativeURL string
	SHA3        string
}

func sqlitePlugin() Plugin {
	return newManifestPlugin(SQLite, pluginHooks{
		download: func(ctx DownloadContext) error {
			return downloadSQLite(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	})
}

func downloadSQLite(client *http.Client, cacheDir, version string) error {
	resolvedVersion, asset, err := resolveSQLiteDownloadAsset(client, version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	return downloadManifestAsset(client, cacheDir, SQLite, version, resolvedVersion, asset)
}

func resolveSQLiteDownloadAsset(client *http.Client, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	product, err := resolveSQLiteDownloadProduct(client, requestedVersion, platformKey(goos, goarch))
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	manifest, err := loadBuiltinManifest(SQLite)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	asset, err := resolveManifestDownloadAsset(SQLite, manifest.Download.Assets, requestedVersion, product.Version, product.Version, goos, goarch, map[string]string{
		"sqlite_numeric":      sqliteNumericVersion(product.Version),
		"sqlite_relative_url": product.RelativeURL,
	})
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	asset.Checksum = product.SHA3

	return product.Version, asset, nil
}

func resolveSQLiteDownloadProduct(client *http.Client, requested, platform string) (sqliteDownloadProduct, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return sqliteDownloadProduct{}, fmt.Errorf("sqlite version cannot be empty")
	}

	page, err := downloadText(client, sqliteDownloadPageURL, "sqlite download page")
	if err != nil {
		return sqliteDownloadProduct{}, err
	}

	products := parseSQLiteDownloadProducts(page)
	sourceFileName := sqliteSourceFileNameForPlatform(platform)
	if sourceFileName == "" {
		return sqliteDownloadProduct{}, fmt.Errorf("sqlite is not available for %s", platform)
	}

	var best *sqliteDownloadProduct
	for index := range products {
		product := &products[index]
		if !strings.Contains(filepath.Base(product.RelativeURL), sourceFileName) {
			continue
		}
		if !versionMatchesRequest(product.Version, requested) {
			continue
		}
		if best == nil || compareVersions(product.Version, best.Version) > 0 {
			best = product
		}
	}
	if best == nil {
		return sqliteDownloadProduct{}, fmt.Errorf("resolve sqlite version %q: no matching release found", requested)
	}

	return *best, nil
}

func parseSQLiteDownloadProducts(page string) []sqliteDownloadProduct {
	products := []sqliteDownloadProduct{}
	for _, line := range strings.Split(page, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "PRODUCT,") {
			continue
		}

		fields := strings.Split(line, ",")
		if len(fields) < 5 {
			continue
		}
		products = append(products, sqliteDownloadProduct{
			Version:     strings.TrimSpace(fields[1]),
			RelativeURL: strings.TrimSpace(fields[2]),
			SHA3:        strings.TrimSpace(fields[4]),
		})
	}

	return products
}

func sqliteSourceFileNameForPlatform(platform string) string {
	switch strings.TrimSpace(platform) {
	case "windows-amd64":
		return "sqlite-tools-win-x64-"
	case "linux-amd64":
		return "sqlite-tools-linux-x64-"
	default:
		return ""
	}
}

func sqliteNumericVersion(version string) string {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) < 3 {
		return strings.ReplaceAll(strings.TrimSpace(version), ".", "")
	}

	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	patch, patchErr := strconv.Atoi(parts[2])
	if majorErr != nil || minorErr != nil || patchErr != nil {
		return strings.ReplaceAll(strings.TrimSpace(version), ".", "")
	}

	return fmt.Sprintf("%d%02d%02d00", major, minor, patch)
}
