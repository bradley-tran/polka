package backend

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	composerDownloadBaseURL = "https://getcomposer.org/download"
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
	default:
		return fmt.Errorf("unsupported tool %q", tool)
	}
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
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open %s for checksum: %w", filePath, err)
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("checksum %s: %w", filePath, err)
	}

	actual := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(strings.TrimSpace(expected), actual) {
		return fmt.Errorf("checksum mismatch for %s: want %s, got %s", filePath, expected, actual)
	}

	return nil
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
