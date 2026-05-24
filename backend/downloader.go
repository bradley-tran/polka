package backend

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha256"
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

const (
	composerDownloadBaseURL = "https://getcomposer.org/download"
	mysqlDownloadBaseURL    = "https://dev.mysql.com/get/Downloads"
	mariadbArchiveBaseURL   = "https://archive.mariadb.org"
	nginxDownloadBaseURL    = "https://nginx.org/download"
	phpWindowsReleaseURL    = "https://windows.php.net/downloads/releases/releases.json"
	phpWindowsBaseURL       = "https://windows.php.net/downloads/releases"
)

var composerReleaseLinkPattern = regexp.MustCompile(`(?:https://getcomposer\.org)?/download/([0-9]+(?:\.[0-9]+){1,2}(?:-[0-9A-Za-z.-]+)?)/composer\.phar`)

type ToolDownloader interface {
	Download(cacheDir, tool, version string) error
}

type HTTPToolDownloader struct {
	Client *http.Client
}

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

type checksumAlgorithm string

const (
	checksumAlgorithmNone   checksumAlgorithm = ""
	checksumAlgorithmMD5    checksumAlgorithm = "md5"
	checksumAlgorithmSHA256 checksumAlgorithm = "sha256"
)

type archiveFormat string

const (
	archiveFormatZip   archiveFormat = "zip"
	archiveFormatTarGz archiveFormat = "tar.gz"
	archiveFormatTarXz archiveFormat = "tar.xz"
)

type databaseDownloadAsset struct {
	FileName          string
	URL               string
	Checksum          string
	ChecksumAlgorithm checksumAlgorithm
	ArchiveFormat     archiveFormat
}

// Keep the initial database downloader deterministic by pinning exact assets
// for the first supported release lines.
var databaseDownloadCatalog = map[string]map[string]map[string]databaseDownloadAsset{
	toolMySQL: {
		"8.4.9": {
			"windows-amd64": {
				FileName:          "mysql-8.4.9-winx64.zip",
				URL:               mysqlDownloadBaseURL + "/MySQL-8.4/mysql-8.4.9-winx64.zip",
				Checksum:          "fe14853279d1704e0f0eb253ea8c8d33",
				ChecksumAlgorithm: checksumAlgorithmMD5,
				ArchiveFormat:     archiveFormatZip,
			},
			"linux-amd64": {
				FileName:          "mysql-8.4.9-linux-glibc2.17-x86_64.tar.xz",
				URL:               mysqlDownloadBaseURL + "/MySQL-8.4/mysql-8.4.9-linux-glibc2.17-x86_64.tar.xz",
				Checksum:          "9d88f7a1b06d6620a92f88b7f5a6050f",
				ChecksumAlgorithm: checksumAlgorithmMD5,
				ArchiveFormat:     archiveFormatTarXz,
			},
		},
	},
	toolMariaDB: {
		"11.4.11": {
			"windows-amd64": {
				FileName:          "mariadb-11.4.11-winx64.zip",
				URL:               mariadbArchiveBaseURL + "/mariadb-11.4.11/winx64-packages/mariadb-11.4.11-winx64.zip",
				Checksum:          "dc8b121a2c0c34a12bd8f4aec37592e00734468aee578030a7f5c971adf68255",
				ChecksumAlgorithm: checksumAlgorithmSHA256,
				ArchiveFormat:     archiveFormatZip,
			},
			"linux-amd64": {
				FileName:          "mariadb-11.4.11-linux-systemd-x86_64.tar.gz",
				URL:               mariadbArchiveBaseURL + "/mariadb-11.4.11/bintar-linux-systemd-x86_64/mariadb-11.4.11-linux-systemd-x86_64.tar.gz",
				Checksum:          "aceffff76d478d462ceb6f4e5f7807c83cf3087f6953c75e0473f9d5aa3cf63e",
				ChecksumAlgorithm: checksumAlgorithmSHA256,
				ArchiveFormat:     archiveFormatTarGz,
			},
		},
		"11.8.7": {
			"windows-amd64": {
				FileName:          "mariadb-11.8.7-winx64.zip",
				URL:               mariadbArchiveBaseURL + "/mariadb-11.8.7/winx64-packages/mariadb-11.8.7-winx64.zip",
				Checksum:          "a613dd4179294dceb023b66bebaea0926c0a89dfb5f6a4d3bc96f63cdb07ea04",
				ChecksumAlgorithm: checksumAlgorithmSHA256,
				ArchiveFormat:     archiveFormatZip,
			},
			"linux-amd64": {
				FileName:          "mariadb-11.8.7-linux-systemd-x86_64.tar.gz",
				URL:               mariadbArchiveBaseURL + "/mariadb-11.8.7/bintar-linux-systemd-x86_64/mariadb-11.8.7-linux-systemd-x86_64.tar.gz",
				Checksum:          "2763b3f21a79732dea55eb093ce6d1c1bd323182d2bc75f40fd0c52fe65e2462",
				ChecksumAlgorithm: checksumAlgorithmSHA256,
				ArchiveFormat:     archiveFormatTarGz,
			},
		},
	},
}

var nginxDownloadCatalog = map[string]map[string]databaseDownloadAsset{
	"1.30.2": {
		"windows-amd64": {
			FileName:          "nginx-1.30.2.zip",
			URL:               nginxDownloadBaseURL + "/nginx-1.30.2.zip",
			ChecksumAlgorithm: checksumAlgorithmNone,
			ArchiveFormat:     archiveFormatZip,
		},
	},
	"1.28.3": {
		"windows-amd64": {
			FileName:          "nginx-1.28.3.zip",
			URL:               nginxDownloadBaseURL + "/nginx-1.28.3.zip",
			ChecksumAlgorithm: checksumAlgorithmNone,
			ArchiveFormat:     archiveFormatZip,
		},
	},
}

func (d HTTPToolDownloader) Download(cacheDir, tool, version string) error {
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}

	switch tool {
	case toolComposer:
		return downloadComposer(client, cacheDir, version)
	case toolPHP:
		return downloadPHP(client, cacheDir, version)
	case toolNginx:
		return downloadNginx(client, cacheDir, version)
	case toolMySQL:
		return downloadMySQL(client, cacheDir, version)
	case toolMariaDB:
		return downloadMariaDB(client, cacheDir, version)
	default:
		return fmt.Errorf("unsupported tool %q", tool)
	}
}

func downloadMySQL(client *http.Client, cacheDir, version string) error {
	return downloadDatabaseTool(client, cacheDir, toolMySQL, version)
}

func downloadMariaDB(client *http.Client, cacheDir, version string) error {
	return downloadDatabaseTool(client, cacheDir, toolMariaDB, version)
}

func downloadNginx(client *http.Client, cacheDir, version string) error {
	_, asset, err := resolveNginxDownloadAsset(version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	return downloadDatabaseAsset(client, cacheDir, toolNginx, version, asset)
}

func downloadDatabaseTool(client *http.Client, cacheDir, tool, version string) error {
	_, asset, err := resolveDatabaseDownloadAsset(tool, version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	return downloadDatabaseAsset(client, cacheDir, tool, version, asset)
}

func resolveDatabaseDownloadAsset(tool, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	requestedVersion = strings.TrimSpace(requestedVersion)
	if requestedVersion == "" {
		return "", databaseDownloadAsset{}, fmt.Errorf("%s version cannot be empty", tool)
	}

	platformKey, err := databasePlatformKey(goos, goarch)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	toolCatalog, ok := databaseDownloadCatalog[tool]
	if !ok {
		return "", databaseDownloadAsset{}, fmt.Errorf("unsupported tool %q", tool)
	}

	resolvedVersion, err := resolveDatabaseCatalogVersion(toolCatalog, requestedVersion)
	if err != nil {
		return "", databaseDownloadAsset{}, fmt.Errorf("resolve %s version %q: %w", tool, requestedVersion, err)
	}

	platformAssets := toolCatalog[resolvedVersion]
	asset, ok := platformAssets[platformKey]
	if !ok {
		return "", databaseDownloadAsset{}, fmt.Errorf("%s version %q is not available for %s/%s", tool, resolvedVersion, goos, goarch)
	}

	return resolvedVersion, asset, nil
}

func resolveNginxDownloadAsset(requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	requestedVersion = strings.TrimSpace(requestedVersion)
	if requestedVersion == "" {
		return "", databaseDownloadAsset{}, fmt.Errorf("%s version cannot be empty", toolNginx)
	}

	platformKey, err := nginxPlatformKey(goos, goarch)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	resolvedVersion, err := resolveDatabaseCatalogVersion(nginxDownloadCatalog, requestedVersion)
	if err != nil {
		return "", databaseDownloadAsset{}, fmt.Errorf("resolve %s version %q: %w", toolNginx, requestedVersion, err)
	}

	platformAssets := nginxDownloadCatalog[resolvedVersion]
	asset, ok := platformAssets[platformKey]
	if !ok {
		return "", databaseDownloadAsset{}, fmt.Errorf("%s version %q is not available for %s/%s", toolNginx, resolvedVersion, goos, goarch)
	}

	return resolvedVersion, asset, nil
}

func databasePlatformKey(goos, goarch string) (string, error) {
	switch {
	case goos == "windows" && goarch == "amd64":
		return "windows-amd64", nil
	case goos == "linux" && goarch == "amd64":
		return "linux-amd64", nil
	default:
		return "", fmt.Errorf("automatic database downloads are only implemented for Windows amd64 and Linux amd64")
	}
}

func nginxPlatformKey(goos, goarch string) (string, error) {
	if goos == "windows" && goarch == "amd64" {
		return "windows-amd64", nil
	}

	return "", fmt.Errorf("automatic nginx downloads are only implemented on Windows amd64")
}

func resolveDatabaseCatalogVersion(catalog map[string]map[string]databaseDownloadAsset, requested string) (string, error) {
	if _, ok := catalog[requested]; ok {
		return requested, nil
	}

	candidates := make([]string, 0, len(catalog))
	for version := range catalog {
		if strings.HasPrefix(version, requested+".") {
			candidates = append(candidates, version)
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no matching release found")
	}

	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if compareCatalogVersions(candidate, best) > 0 {
			best = candidate
		}
	}

	return best, nil
}

func compareCatalogVersions(left, right string) int {
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

func downloadDatabaseAsset(client *http.Client, cacheDir, tool, version string, asset databaseDownloadAsset) error {
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
	if err := verifyFileChecksum(asset.ChecksumAlgorithm, asset.Checksum, archivePath); err != nil {
		return err
	}
	if err := extractArchive(archivePath, payloadDir, asset.ArchiveFormat); err != nil {
		return err
	}
	if err := collapseSingleDirectory(payloadDir); err != nil {
		return err
	}

	return finalizeCacheVersion(cacheVersionDir, payloadDir)
}

func downloadComposer(client *http.Client, cacheDir, version string) error {
	if err := os.MkdirAll(filepath.Join(cacheDir, toolComposer), 0o755); err != nil {
		return fmt.Errorf("create composer cache dir: %w", err)
	}

	cacheVersionDir := filepath.Join(cacheDir, toolComposer, version)
	stagingDir, err := os.MkdirTemp(filepath.Join(cacheDir, toolComposer), version+"-tmp-")
	if err != nil {
		return fmt.Errorf("create composer staging dir: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	targetPath := filepath.Join(stagingDir, "bin", "composer.phar")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create composer target dir: %w", err)
	}

	url, checksumURL, err := resolveComposerDownloadURLs(client, version)
	if err != nil {
		return err
	}
	if err := downloadFile(client, url, targetPath); err != nil {
		return err
	}
	if err := verifyFileSHA256(client, targetPath, checksumURL); err != nil {
		return err
	}

	return finalizeCacheVersion(cacheVersionDir, stagingDir)
}

func resolveComposerDownloadURLs(client *http.Client, version string) (string, string, error) {
	resolvedVersion := strings.TrimSpace(version)
	if composerVersionNeedsResolution(resolvedVersion) {
		var err error
		resolvedVersion, err = resolveComposerReleaseVersion(client, resolvedVersion)
		if err != nil {
			return "", "", err
		}
	}

	url := fmt.Sprintf("%s/%s/composer.phar", composerDownloadBaseURL, resolvedVersion)
	return url, url + ".sha256sum", nil
}

func composerVersionNeedsResolution(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}

	return !strings.Contains(version, "-") && strings.Count(version, ".") < 2
}

func resolveComposerReleaseVersion(client *http.Client, requested string) (string, error) {
	response, err := client.Get(composerDownloadBaseURL + "/")
	if err != nil {
		return "", fmt.Errorf("download composer release index: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download composer release index: unexpected status %s", response.Status)
	}

	page, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("read composer release index: %w", err)
	}

	resolvedVersion, err := selectComposerReleaseVersion(string(page), requested)
	if err != nil {
		return "", fmt.Errorf("resolve composer version %q: %w", requested, err)
	}

	return resolvedVersion, nil
}

func selectComposerReleaseVersion(page, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("requested composer version is empty")
	}

	allowPrerelease := strings.Contains(requested, "-")
	seen := map[string]struct{}{}
	for _, match := range composerReleaseLinkPattern.FindAllStringSubmatch(page, -1) {
		version := strings.TrimSpace(match[1])
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}

		if version != requested && !strings.HasPrefix(version, requested+".") {
			continue
		}
		if !allowPrerelease && strings.Contains(version, "-") {
			continue
		}

		return version, nil
	}

	return "", fmt.Errorf("no matching release found")
}

func downloadPHP(client *http.Client, cacheDir, version string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("automatic php download is only implemented on Windows")
	}
	if err := os.MkdirAll(filepath.Join(cacheDir, toolPHP), 0o755); err != nil {
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

	cacheVersionDir := filepath.Join(cacheDir, toolPHP, version)
	stagingDir, err := os.MkdirTemp(filepath.Join(cacheDir, toolPHP), version+"-tmp-")
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
	response, err := client.Get(url)
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

func verifyFileSHA256(client *http.Client, filePath, checksumURL string) error {
	response, err := client.Get(checksumURL)
	if err != nil {
		return fmt.Errorf("download checksum %s: %w", checksumURL, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download checksum %s: unexpected status %s", checksumURL, response.Status)
	}

	checksumData, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("read checksum %s: %w", checksumURL, err)
	}

	expected, err := parseChecksumValue(string(checksumData))
	if err != nil {
		return fmt.Errorf("parse checksum %s: %w", checksumURL, err)
	}

	return verifyChecksum(expected, filePath)
}

func parseChecksumValue(value string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty checksum response")
	}

	checksum := fields[0]
	if len(checksum) != sha256.Size*2 {
		return "", fmt.Errorf("invalid checksum length %d", len(checksum))
	}
	if _, err := hex.DecodeString(checksum); err != nil {
		return "", fmt.Errorf("invalid checksum encoding: %w", err)
	}

	return checksum, nil
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
