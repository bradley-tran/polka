package tools

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

var (
	apacheLoungeDownloadURL         = "https://www.apachelounge.com/download/"
	apacheLoungeDownloadPattern     = regexp.MustCompile(`(?i)httpd-([0-9]+\.[0-9]+\.[0-9]+)-([0-9]+)-Win64-VS([0-9]+)\.zip`)
	apacheLoungeDownloadHrefPattern = regexp.MustCompile(`(?i)href=["']([^"']*?(httpd-([0-9]+\.[0-9]+\.[0-9]+)-([0-9]+)-Win64-VS([0-9]+)\.zip))["']`)
)

type apacheLoungeRelease struct {
	Version     string
	Build       string
	VS          string
	URL         string
	ChecksumURL string
}

func apachePlugin() Plugin {
	return newManifestPlugin(Apache, pluginHooks{
		download: func(ctx DownloadContext) error {
			return downloadApache(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	})
}

func downloadApache(client *http.Client, cacheDir, version string) error {
	resolvedVersion, asset, err := resolveApacheDownloadAsset(client, version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	return downloadManifestAsset(client, cacheDir, Apache, version, resolvedVersion, asset)
}

func resolveApacheDownloadAsset(client *http.Client, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	release, err := resolveApacheLoungeRelease(client, requestedVersion)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	manifest, err := loadBuiltinManifest(Apache)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	asset, err := resolveManifestDownloadAsset(Apache, manifest.Download.Assets, requestedVersion, release.Version, release.Version, goos, goarch, map[string]string{
		"build": release.Build,
		"vs":    release.VS,
	})
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}
	applyApacheLoungeReleaseAsset(&asset, release)

	return release.Version, asset, nil
}

func resolveApacheLoungeRelease(client *http.Client, requested string) (apacheLoungeRelease, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return apacheLoungeRelease{}, fmt.Errorf("apache version cannot be empty")
	}

	page, err := downloadText(client, apacheLoungeDownloadURL, "apache lounge download page")
	if err != nil {
		return apacheLoungeRelease{}, err
	}

	best := apacheLoungeRelease{}
	for _, release := range apacheLoungePageReleases(page) {
		if !versionMatchesRequest(release.Version, requested) {
			continue
		}
		if best.Version == "" || compareApacheLoungeRelease(release, best) > 0 {
			best = release
		}
	}
	if best.Version == "" {
		return apacheLoungeRelease{}, fmt.Errorf("resolve apache version %q: no matching Apache Lounge release found", requested)
	}

	return best, nil
}

// apacheLoungePageReleases prefers real archive hrefs so the downloader follows
// the path Apache Lounge publishes instead of rebuilding it from a fixed shape.
func apacheLoungePageReleases(page string) []apacheLoungeRelease {
	seen := map[string]struct{}{}
	releases := make([]apacheLoungeRelease, 0)
	for _, match := range apacheLoungeDownloadHrefPattern.FindAllStringSubmatch(page, -1) {
		release := apacheLoungeRelease{
			Version: strings.TrimSpace(match[3]),
			Build:   strings.TrimSpace(match[4]),
			VS:      strings.TrimSpace(match[5]),
		}
		if release.Version == "" || release.Build == "" || release.VS == "" {
			continue
		}
		release.URL = resolveApacheLoungePageURL(match[1])
		if release.URL != "" {
			release.ChecksumURL = release.URL + ".txt"
		}

		key := apacheLoungeReleaseKey(release)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		releases = append(releases, release)
	}

	for _, match := range apacheLoungeDownloadPattern.FindAllStringSubmatch(page, -1) {
		release := apacheLoungeRelease{
			Version: strings.TrimSpace(match[1]),
			Build:   strings.TrimSpace(match[2]),
			VS:      strings.TrimSpace(match[3]),
		}
		if release.Version == "" || release.Build == "" || release.VS == "" {
			continue
		}

		key := apacheLoungeReleaseKey(release)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		releases = append(releases, release)
	}

	return releases
}

func apacheLoungeReleaseKey(release apacheLoungeRelease) string {
	return release.Version + "\x00" + release.Build + "\x00" + release.VS
}

// resolveApacheLoungePageURL resolves relative links against the configured
// Apache Lounge download page, which tests override with a local server URL.
func resolveApacheLoungePageURL(rawURL string) string {
	rawURL = strings.TrimSpace(html.UnescapeString(rawURL))
	if rawURL == "" {
		return ""
	}

	baseURL, err := url.Parse(apacheLoungeDownloadURL)
	if err != nil {
		return rawURL
	}
	referenceURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	return baseURL.ResolveReference(referenceURL).String()
}

// applyApacheLoungeReleaseAsset attaches the live archive URL and checksum
// sidecar discovered from the Apache Lounge listing to the manifest asset.
func applyApacheLoungeReleaseAsset(asset *downloadAsset, release apacheLoungeRelease) {
	if strings.TrimSpace(release.URL) != "" {
		asset.URL = strings.TrimSpace(release.URL)
	}
	if strings.TrimSpace(release.ChecksumURL) != "" {
		asset.ChecksumURL = strings.TrimSpace(release.ChecksumURL)
		asset.ChecksumAlgorithm = checksumAlgorithmSHA256
	}
}

func compareApacheLoungeRelease(left, right apacheLoungeRelease) int {
	if comparison := compareVersions(left.Version, right.Version); comparison != 0 {
		return comparison
	}
	if comparison := compareNumericString(left.Build, right.Build); comparison != 0 {
		return comparison
	}
	return compareNumericString(left.VS, right.VS)
}

func compareNumericString(left, right string) int {
	leftValue, leftErr := strconv.Atoi(strings.TrimSpace(left))
	rightValue, rightErr := strconv.Atoi(strings.TrimSpace(right))
	if leftErr == nil && rightErr == nil {
		switch {
		case leftValue > rightValue:
			return 1
		case leftValue < rightValue:
			return -1
		default:
			return 0
		}
	}

	return strings.Compare(strings.TrimSpace(left), strings.TrimSpace(right))
}
