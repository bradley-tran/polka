package tools

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha3"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

type Downloader interface {
	Download(cacheDir, tool, version string) error
}

type HTTPDownloader struct {
	Client  *http.Client
	Plugins *Registry
}

type checksumAlgorithm string

const (
	checksumAlgorithmNone     checksumAlgorithm = ""
	checksumAlgorithmMD5      checksumAlgorithm = "md5"
	checksumAlgorithmSHA256   checksumAlgorithm = "sha256"
	checksumAlgorithmSHA3_256 checksumAlgorithm = "sha3-256"
)

type archiveFormat string

const (
	archiveFormatZip   archiveFormat = "zip"
	archiveFormatTarGz archiveFormat = "tar.gz"
	archiveFormatTarXz archiveFormat = "tar.xz"
)

var (
	downloadTemplatePattern = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)
	githubAPIBaseURL        = "https://api.github.com"
)

func (d HTTPDownloader) Download(cacheDir, tool, version string) error {
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}

	registry := d.Plugins
	if registry == nil {
		registry = NewDefaultRegistry()
	}
	plugin, ok := registry.Plugin(tool)
	if !ok {
		return fmt.Errorf("unsupported tool %q", tool)
	}

	return plugin.Download(DownloadContext{
		Client:   client,
		CacheDir: cacheDir,
		Tool:     tool,
		Version:  version,
	})
}

func downloadBuiltinManifestTool(client *http.Client, cacheDir, tool, version string) error {
	manifest, err := loadBuiltinManifest(tool)
	if err != nil {
		return err
	}

	return downloadManifestAssetForRequest(client, cacheDir, tool, version, manifest.Download, runtime.GOOS, runtime.GOARCH)
}

func downloadBuiltinManifestToolResolved(client *http.Client, cacheDir, tool, requestedVersion, resolvedVersion string, values map[string]string) error {
	return downloadBuiltinManifestToolResolvedWithTag(client, cacheDir, tool, requestedVersion, resolvedVersion, resolvedVersion, values)
}

func downloadBuiltinManifestToolResolvedWithTag(client *http.Client, cacheDir, tool, requestedVersion, resolvedVersion, tag string, values map[string]string) error {
	manifest, err := loadBuiltinManifest(tool)
	if err != nil {
		return err
	}

	asset, err := resolveManifestDownloadAsset(tool, manifest.Download.Assets, requestedVersion, resolvedVersion, tag, runtime.GOOS, runtime.GOARCH, values)
	if err != nil {
		return err
	}

	return downloadManifestAsset(client, cacheDir, tool, requestedVersion, asset)
}

func resolveBuiltinManifestDownloadAsset(tool, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	manifest, err := loadBuiltinManifest(tool)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	asset, err := resolveManifestDownloadAsset(tool, manifest.Download.Assets, requestedVersion, requestedVersion, requestedVersion, goos, goarch, nil)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	return requestedVersion, asset, nil
}

func downloadManifestAssetForRequest(client *http.Client, cacheDir, tool, requestedVersion string, download manifestDownload, goos, goarch string) error {
	resolvedVersion, tag, githubAssets, err := resolveManifestDownloadVersion(client, tool, requestedVersion, download)
	if err != nil {
		return err
	}

	asset, err := resolveManifestDownloadAsset(tool, download.Assets, requestedVersion, resolvedVersion, tag, goos, goarch, nil)
	if err != nil {
		return err
	}
	if len(githubAssets) > 0 {
		applyGitHubAssetDigest(&asset, githubAssets)
	}

	return downloadManifestAsset(client, cacheDir, tool, requestedVersion, asset)
}

func resolveManifestDownloadVersion(client *http.Client, tool, requestedVersion string, download manifestDownload) (string, string, []githubReleaseAsset, error) {
	requestedVersion = strings.TrimSpace(requestedVersion)
	if requestedVersion == "" {
		return "", "", nil, fmt.Errorf("%s version cannot be empty", tool)
	}
	if !download.hasGitHub() {
		return requestedVersion, requestedVersion, nil, nil
	}

	version, tag, assets, err := resolveGitHubReleaseVersion(client, download.GitHub, requestedVersion)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve %s version %q: %w", tool, requestedVersion, err)
	}

	return version, tag, assets, nil
}

func resolveManifestDownloadAsset(tool string, assets map[string]downloadAsset, requestedVersion, resolvedVersion, tag, goos, goarch string, values map[string]string) (databaseDownloadAsset, error) {
	if len(assets) == 0 {
		return databaseDownloadAsset{}, fmt.Errorf("%s does not define automatic downloads", tool)
	}

	for _, key := range downloadPlatformKeys(goos, goarch) {
		if asset, ok := assets[key]; ok {
			renderedAsset, err := renderDownloadAsset(asset, requestedVersion, resolvedVersion, tag, values)
			if err != nil {
				return databaseDownloadAsset{}, fmt.Errorf("render %s download asset for %s: %w", tool, key, err)
			}

			return renderedAsset, nil
		}
	}

	return databaseDownloadAsset{}, fmt.Errorf("%s version %q is not available for %s/%s", tool, resolvedVersion, goos, goarch)
}

func downloadPlatformKeys(goos, goarch string) []string {
	keys := []string{}
	if strings.TrimSpace(goos) != "" && strings.TrimSpace(goarch) != "" {
		keys = append(keys, platformKey(goos, goarch))
	}
	keys = append(keys, "all")
	return keys
}

func platformKey(goos, goarch string) string {
	return strings.TrimSpace(goos) + "-" + strings.TrimSpace(goarch)
}

func renderDownloadAsset(asset downloadAsset, requestedVersion, resolvedVersion, tag string, values map[string]string) (downloadAsset, error) {
	templateValues := map[string]string{
		"requested": strings.TrimSpace(requestedVersion),
		"version":   strings.TrimSpace(resolvedVersion),
		"tag":       strings.TrimSpace(tag),
	}
	if templateValues["tag"] == "" {
		templateValues["tag"] = templateValues["version"]
	}
	for key, value := range values {
		templateValues[key] = value
	}

	var err error
	asset.FileName, err = renderDownloadTemplate("filename", asset.FileName, templateValues)
	if err != nil {
		return downloadAsset{}, err
	}
	asset.URL, err = renderDownloadTemplate("url", asset.URL, templateValues)
	if err != nil {
		return downloadAsset{}, err
	}
	asset.ChecksumURL, err = renderDownloadTemplate("checksum-url", asset.ChecksumURL, templateValues)
	if err != nil {
		return downloadAsset{}, err
	}
	asset.SourceFileName, err = renderDownloadTemplate("source-filename", asset.SourceFileName, templateValues)
	if err != nil {
		return downloadAsset{}, err
	}
	if strings.TrimSpace(asset.SourceFileName) == "" {
		asset.SourceFileName = asset.FileName
	}

	return asset, nil
}

func renderDownloadTemplate(field, value string, values map[string]string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return strings.TrimSpace(value), nil
	}

	missing := ""
	rendered := downloadTemplatePattern.ReplaceAllStringFunc(value, func(match string) string {
		key := match[1 : len(match)-1]
		replacement, ok := values[key]
		if !ok {
			missing = key
			return match
		}
		return replacement
	})
	if missing != "" {
		return "", fmt.Errorf("%s references unknown template value %q", field, missing)
	}

	return rendered, nil
}

func resolveMatchingVersion(versions []string, requested string) (string, error) {
	requested = strings.TrimSpace(strings.TrimPrefix(requested, "v"))
	if requested == "" {
		return "", fmt.Errorf("requested version is empty")
	}

	candidates := make([]string, 0, len(versions))
	for _, version := range versions {
		normalizedVersion := strings.TrimSpace(strings.TrimPrefix(version, "v"))
		if versionMatchesRequest(normalizedVersion, requested) {
			candidates = append(candidates, normalizedVersion)
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no matching release found")
	}

	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if compareVersions(candidate, best) > 0 {
			best = candidate
		}
	}

	return best, nil
}

func versionMatchesRequest(version, requested string) bool {
	return version == requested || strings.HasPrefix(version, requested+".")
}

func versionNeedsResolution(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}

	return !strings.Contains(version, "-") && strings.Count(version, ".") < 2
}

func versionMajorMinor(version string) string {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}

	return strings.TrimSpace(version)
}

func compareVersions(left, right string) int {
	leftParts := strings.Split(strings.TrimSpace(left), ".")
	rightParts := strings.Split(strings.TrimSpace(right), ".")
	count := len(leftParts)
	if len(rightParts) > count {
		count = len(rightParts)
	}

	for index := 0; index < count; index++ {
		leftPart := "0"
		if index < len(leftParts) {
			leftPart = leftParts[index]
		}
		rightPart := "0"
		if index < len(rightParts) {
			rightPart = rightParts[index]
		}

		leftValue, leftErr := strconv.Atoi(leftPart)
		rightValue, rightErr := strconv.Atoi(rightPart)
		switch {
		case leftErr == nil && rightErr == nil:
			if leftValue > rightValue {
				return 1
			}
			if leftValue < rightValue {
				return -1
			}
		default:
			comparison := strings.Compare(leftPart, rightPart)
			if comparison != 0 {
				return comparison
			}
		}
	}

	return 0
}

type githubRelease struct {
	TagName    string               `json:"tag_name"`
	Draft      bool                 `json:"draft"`
	Prerelease bool                 `json:"prerelease"`
	Assets     []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

func resolveGitHubReleaseVersion(client *http.Client, config manifestGitHubDownload, requested string) (string, string, []githubReleaseAsset, error) {
	requested = strings.TrimPrefix(strings.TrimSpace(requested), strings.TrimSpace(config.TagPrefix))
	releases, err := fetchGitHubReleases(client, config)
	if err != nil {
		return "", "", nil, err
	}

	allowPrerelease := strings.Contains(strings.TrimSpace(requested), "-")
	var selected *githubRelease
	selectedVersion := ""
	for index := range releases {
		release := &releases[index]
		if release.Draft {
			continue
		}
		if release.Prerelease && !allowPrerelease {
			continue
		}

		version := versionFromGitHubTag(release.TagName, config.TagPrefix)
		if !versionMatchesRequest(version, requested) {
			continue
		}
		if selected == nil || compareVersions(version, selectedVersion) > 0 {
			selected = release
			selectedVersion = version
		}
	}
	if selected == nil {
		return "", "", nil, fmt.Errorf("no matching release found")
	}

	return selectedVersion, strings.TrimSpace(selected.TagName), selected.Assets, nil
}

func fetchGitHubReleases(client *http.Client, config manifestGitHubDownload) ([]githubRelease, error) {
	owner := strings.TrimSpace(config.Owner)
	repo := strings.TrimSpace(config.Repo)
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("github download resolver requires owner and repo")
	}

	releases := []githubRelease{}
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=100&page=%d", strings.TrimRight(githubAPIBaseURL, "/"), owner, repo, page)
		var pageReleases []githubRelease
		if err := downloadJSON(client, url, "github releases", &pageReleases); err != nil {
			return nil, err
		}
		releases = append(releases, pageReleases...)
		if len(pageReleases) < 100 {
			break
		}
	}

	return releases, nil
}

func versionFromGitHubTag(tag, tagPrefix string) string {
	version := strings.TrimSpace(tag)
	prefix := strings.TrimSpace(tagPrefix)
	if prefix != "" {
		version = strings.TrimPrefix(version, prefix)
	}

	return strings.TrimSpace(version)
}

func applyGitHubAssetDigest(asset *downloadAsset, githubAssets []githubReleaseAsset) {
	sourceFileName := normalizeChecksumFileName(asset.SourceFileName)
	for _, githubAsset := range githubAssets {
		if normalizeChecksumFileName(githubAsset.Name) != sourceFileName {
			continue
		}

		algorithm, checksum, ok := parseGitHubAssetDigest(githubAsset.Digest)
		if !ok {
			return
		}
		if asset.ChecksumAlgorithm != checksumAlgorithmNone && asset.ChecksumAlgorithm != algorithm {
			return
		}

		asset.ChecksumAlgorithm = algorithm
		asset.Checksum = checksum
		return
	}
}

func parseGitHubAssetDigest(digest string) (checksumAlgorithm, string, bool) {
	algorithmText, checksum, ok := strings.Cut(strings.TrimSpace(digest), ":")
	if !ok {
		return checksumAlgorithmNone, "", false
	}

	algorithm := checksumAlgorithm(strings.ToLower(strings.TrimSpace(algorithmText)))
	switch algorithm {
	case checksumAlgorithmMD5, checksumAlgorithmSHA256, checksumAlgorithmSHA3_256:
	default:
		return checksumAlgorithmNone, "", false
	}

	if _, _, err := validateChecksumValue(algorithm, checksum); err != nil {
		return checksumAlgorithmNone, "", false
	}

	return algorithm, strings.TrimSpace(checksum), true
}

func downloadDatabaseAsset(client *http.Client, cacheDir, tool, version string, asset databaseDownloadAsset) error {
	return downloadManifestAsset(client, cacheDir, tool, version, asset)
}

func downloadManifestAsset(client *http.Client, cacheDir, tool, version string, asset downloadAsset) error {
	if err := os.MkdirAll(filepath.Join(cacheDir, tool), 0o755); err != nil {
		return fmt.Errorf("create %s cache dir: %w", tool, err)
	}

	cacheVersionDir := filepath.Join(cacheDir, tool, version)
	stagingDir, err := os.MkdirTemp(filepath.Join(cacheDir, tool), version+"-tmp-")
	if err != nil {
		return fmt.Errorf("create %s staging dir: %w", tool, err)
	}
	defer os.RemoveAll(stagingDir)

	payloadDir := filepath.Join(stagingDir, "payload")
	if err := os.MkdirAll(payloadDir, 0o755); err != nil {
		return fmt.Errorf("create %s payload dir: %w", tool, err)
	}

	archivePath := filepath.Join(stagingDir, asset.FileName)
	if err := downloadFile(client, asset.URL, archivePath); err != nil {
		return err
	}
	checksum := strings.TrimSpace(asset.Checksum)
	if checksum == "" && strings.TrimSpace(asset.ChecksumURL) != "" {
		var err error
		checksum, err = downloadChecksumValue(client, asset.ChecksumURL, asset.ChecksumAlgorithm, asset.SourceFileName)
		if err != nil {
			return err
		}
	}
	if checksum != "" {
		if err := verifyFileChecksum(asset.ChecksumAlgorithm, checksum, archivePath); err != nil {
			return err
		}
	}
	if err := extractArchive(archivePath, payloadDir, asset.ArchiveFormat); err != nil {
		return err
	}
	if err := collapseSingleDirectory(payloadDir); err != nil {
		return err
	}

	return finalizeCacheVersion(cacheVersionDir, payloadDir)
}

func finalizeCacheVersion(targetDir, stagingDir string) error {
	if err := os.RemoveAll(targetDir); err != nil {
		return fmt.Errorf("reset cache version directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
		return fmt.Errorf("create cache parent dir: %w", err)
	}
	if err := os.Rename(stagingDir, targetDir); err != nil {
		return fmt.Errorf("finalize cache version: %w", err)
	}

	return nil
}

func extractArchive(archivePath, targetDir string, format archiveFormat) error {
	switch format {
	case archiveFormatZip:
		return extractZipArchive(archivePath, targetDir)
	case archiveFormatTarGz:
		return extractTarGzipArchive(archivePath, targetDir)
	case archiveFormatTarXz:
		return extractTarXZArchive(archivePath, targetDir)
	default:
		return fmt.Errorf("unsupported archive format %q", format)
	}
}

func extractTarGzipArchive(archivePath, targetDir string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tar.gz archive %s: %w", archivePath, err)
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open gzip archive %s: %w", archivePath, err)
	}
	defer gzipReader.Close()

	return extractTarStream(tar.NewReader(gzipReader), targetDir)
}

func extractTarXZArchive(archivePath, targetDir string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tar.xz archive %s: %w", archivePath, err)
	}
	defer file.Close()

	xzReader, err := xz.NewReader(file)
	if err != nil {
		return fmt.Errorf("open xz archive %s: %w", archivePath, err)
	}

	return extractTarStream(tar.NewReader(xzReader), targetDir)
}

func extractTarStream(reader *tar.Reader, targetDir string) error {
	cleanTarget := filepath.Clean(targetDir)
	if err := os.MkdirAll(cleanTarget, 0o755); err != nil {
		return fmt.Errorf("create target dir %s: %w", cleanTarget, err)
	}

	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}

		destinationPath, err := archiveDestinationPath(cleanTarget, header.Name)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destinationPath, header.FileInfo().Mode()); err != nil {
				return fmt.Errorf("create directory %s: %w", destinationPath, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
				return fmt.Errorf("create parent dir for %s: %w", destinationPath, err)
			}

			target, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, header.FileInfo().Mode())
			if err != nil {
				return fmt.Errorf("create extracted file %s: %w", destinationPath, err)
			}

			_, copyErr := io.Copy(target, reader)
			target.Close()
			if copyErr != nil {
				return fmt.Errorf("extract tar entry %s: %w", header.Name, copyErr)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
				return fmt.Errorf("create parent dir for %s: %w", destinationPath, err)
			}

			linkTarget := filepath.Clean(filepath.Join(filepath.Dir(destinationPath), header.Linkname))
			if linkTarget != cleanTarget && !strings.HasPrefix(linkTarget, cleanTarget+string(os.PathSeparator)) {
				return fmt.Errorf("tar symlink %s escapes target directory", header.Name)
			}
			if err := os.Symlink(header.Linkname, destinationPath); err != nil {
				return fmt.Errorf("create symlink %s: %w", destinationPath, err)
			}
		case tar.TypeLink:
			if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
				return fmt.Errorf("create parent dir for %s: %w", destinationPath, err)
			}

			linkTarget, err := archiveDestinationPath(cleanTarget, header.Linkname)
			if err != nil {
				return err
			}
			if err := os.Link(linkTarget, destinationPath); err != nil {
				return fmt.Errorf("create hard link %s: %w", destinationPath, err)
			}
		default:
			return fmt.Errorf("unsupported tar entry %s with type %d", header.Name, header.Typeflag)
		}
	}
}

func archiveDestinationPath(targetDir, entryName string) (string, error) {
	cleanTarget := filepath.Clean(targetDir)
	destinationPath := filepath.Join(cleanTarget, entryName)
	cleanDestination := filepath.Clean(destinationPath)
	if cleanDestination != cleanTarget && !strings.HasPrefix(cleanDestination, cleanTarget+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry %s escapes target directory", entryName)
	}

	return cleanDestination, nil
}

func collapseSingleDirectory(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read extracted root %s: %w", root, err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return nil
	}

	nestedRoot := filepath.Join(root, entries[0].Name())
	nestedEntries, err := os.ReadDir(nestedRoot)
	if err != nil {
		return fmt.Errorf("read nested archive root %s: %w", nestedRoot, err)
	}

	for _, entry := range nestedEntries {
		if err := os.Rename(filepath.Join(nestedRoot, entry.Name()), filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf("flatten archive root %s: %w", nestedRoot, err)
		}
	}

	if err := os.Remove(nestedRoot); err != nil {
		return fmt.Errorf("remove nested archive root %s: %w", nestedRoot, err)
	}

	return nil
}

func downloadFile(client *http.Client, url, targetPath string) error {
	client = effectiveHTTPClient(client)
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	setDownloadRequestHeaders(request)

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %s", url, response.Status)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create parent dir for %s: %w", targetPath, err)
	}

	file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", targetPath, err)
	}
	defer file.Close()

	if _, err := io.Copy(file, response.Body); err != nil {
		return fmt.Errorf("write %s: %w", targetPath, err)
	}

	return nil
}

func downloadText(client *http.Client, url, description string) (string, error) {
	client = effectiveHTTPClient(client)
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("download %s %s: %w", description, url, err)
	}
	setDownloadRequestHeaders(request)

	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download %s %s: %w", description, url, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s %s: unexpected status %s", description, url, response.Status)
	}

	data, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("read %s %s: %w", description, url, err)
	}

	return string(data), nil
}

func downloadJSON(client *http.Client, url, description string, target any) error {
	client = effectiveHTTPClient(client)
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("download %s %s: %w", description, url, err)
	}
	setDownloadRequestHeaders(request)
	request.Header.Set("Accept", "application/vnd.github+json")

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s %s: %w", description, url, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s %s: unexpected status %s", description, url, response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode %s %s: %w", description, url, err)
	}

	return nil
}

func setDownloadRequestHeaders(request *http.Request) {
	request.Header.Set("User-Agent", "polka")
}

func effectiveHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}

	return &http.Client{Timeout: 10 * time.Minute}
}

func downloadChecksumValue(client *http.Client, checksumURL string, algorithm checksumAlgorithm, sourceFileName string) (string, error) {
	checksumData, err := downloadText(client, checksumURL, "checksum")
	if err != nil {
		return "", err
	}

	checksum, err := parseChecksumValueForAlgorithm(algorithm, checksumData, sourceFileName)
	if err != nil {
		return "", fmt.Errorf("parse checksum %s: %w", checksumURL, err)
	}

	return checksum, nil
}

func verifyFileSHA256(client *http.Client, filePath, checksumURL string) error {
	checksumData, err := downloadText(client, checksumURL, "checksum")
	if err != nil {
		return err
	}

	expected, err := parseChecksumValue(checksumData)
	if err != nil {
		return fmt.Errorf("parse checksum %s: %w", checksumURL, err)
	}

	return verifyChecksum(expected, filePath)
}

func parseChecksumValue(value string) (string, error) {
	return parseChecksumValueForAlgorithm(checksumAlgorithmSHA256, value, "")
}

func parseChecksumValueForAlgorithm(algorithm checksumAlgorithm, value, sourceFileName string) (string, error) {
	normalizedSourceFileName := normalizeChecksumFileName(sourceFileName)
	lines := strings.Split(strings.TrimSpace(value), "\n")
	firstChecksum := ""
	for _, line := range lines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}

		checksum := fields[0]
		if firstChecksum == "" {
			firstChecksum = checksum
		}
		if normalizedSourceFileName == "" || checksumLineMatchesSource(fields[1:], normalizedSourceFileName) {
			normalizedChecksum, _, err := validateChecksumValue(algorithm, checksum)
			if err != nil {
				return "", err
			}
			return normalizedChecksum, nil
		}
	}
	if normalizedSourceFileName != "" {
		return "", fmt.Errorf("no checksum found for %s", sourceFileName)
	}
	if firstChecksum == "" {
		return "", fmt.Errorf("empty checksum response")
	}

	normalizedChecksum, _, err := validateChecksumValue(algorithm, firstChecksum)
	return normalizedChecksum, err
}

func checksumLineMatchesSource(fields []string, sourceFileName string) bool {
	for _, field := range fields {
		if normalizeChecksumFileName(field) == sourceFileName {
			return true
		}
	}

	return false
}

func normalizeChecksumFileName(fileName string) string {
	normalized := strings.TrimSpace(fileName)
	if normalized == "" {
		return ""
	}
	normalized = strings.TrimPrefix(normalized, "*")
	normalized = strings.TrimPrefix(normalized, "./")
	normalized = strings.ReplaceAll(normalized, "\\", "/")
	return strings.TrimSpace(filepath.Base(normalized))
}

func validateChecksumValue(algorithm checksumAlgorithm, checksum string) (string, int, error) {
	_, checksumSize, err := checksumHasher(algorithm)
	if err != nil {
		return "", 0, err
	}

	checksum = strings.TrimSpace(checksum)
	if len(checksum) != checksumSize*2 {
		return "", 0, fmt.Errorf("invalid %s checksum length %d", algorithm, len(checksum))
	}
	if _, err := hex.DecodeString(checksum); err != nil {
		return "", 0, fmt.Errorf("invalid %s checksum encoding: %w", algorithm, err)
	}

	return checksum, checksumSize, nil
}

func verifyChecksum(expected, filePath string) error {
	return verifyFileChecksum(checksumAlgorithmSHA256, expected, filePath)
}

func verifyFileChecksum(algorithm checksumAlgorithm, expected, filePath string) error {
	if algorithm == checksumAlgorithmNone {
		return nil
	}

	newHasher, checksumSize, err := checksumHasher(algorithm)
	if err != nil {
		return err
	}

	expected = strings.TrimSpace(expected)
	if len(expected) != checksumSize*2 {
		return fmt.Errorf("invalid %s checksum length %d", algorithm, len(expected))
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return fmt.Errorf("invalid %s checksum encoding: %w", algorithm, err)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open %s for checksum: %w", filePath, err)
	}
	defer file.Close()

	hasher := newHasher()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("checksum %s: %w", filePath, err)
	}

	actual := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(strings.TrimSpace(expected), actual) {
		return fmt.Errorf("checksum mismatch for %s: want %s, got %s", filePath, expected, actual)
	}

	return nil
}

func checksumHasher(algorithm checksumAlgorithm) (func() hash.Hash, int, error) {
	switch algorithm {
	case checksumAlgorithmMD5:
		return md5.New, md5.Size, nil
	case checksumAlgorithmSHA256:
		return sha256.New, sha256.Size, nil
	case checksumAlgorithmSHA3_256:
		return func() hash.Hash { return sha3.New256() }, sha256.Size, nil
	default:
		return nil, 0, fmt.Errorf("unsupported checksum algorithm %q", algorithm)
	}
}

func extractZipArchive(archivePath, targetDir string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip archive %s: %w", archivePath, err)
	}
	defer reader.Close()

	cleanTarget := filepath.Clean(targetDir)
	for _, file := range reader.File {
		destinationPath := filepath.Join(cleanTarget, file.Name)
		if !strings.HasPrefix(filepath.Clean(destinationPath), cleanTarget+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry %s escapes target directory", file.Name)
		}

		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(destinationPath, file.Mode()); err != nil {
				return fmt.Errorf("create directory %s: %w", destinationPath, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
			return fmt.Errorf("create parent dir for %s: %w", destinationPath, err)
		}

		source, err := file.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %s: %w", file.Name, err)
		}

		target, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, file.Mode())
		if err != nil {
			source.Close()
			return fmt.Errorf("create extracted file %s: %w", destinationPath, err)
		}

		_, copyErr := io.Copy(target, source)
		source.Close()
		target.Close()
		if copyErr != nil {
			return fmt.Errorf("extract zip entry %s: %w", file.Name, copyErr)
		}
	}

	return nil
}
