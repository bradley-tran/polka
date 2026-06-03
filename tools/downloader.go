package tools

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

	return downloadManifestCatalogAsset(client, cacheDir, tool, version, manifest.Download.Catalog, runtime.GOOS, runtime.GOARCH)
}

func downloadManifestCatalogAsset(client *http.Client, cacheDir, tool, version string, catalog downloadCatalog, goos, goarch string) error {
	_, asset, err := resolveDownloadCatalogAsset(tool, catalog, version, goos, goarch)
	if err != nil {
		return err
	}

	return downloadManifestAsset(client, cacheDir, tool, version, asset)
}

func resolveBuiltinManifestDownloadAsset(tool, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	manifest, err := loadBuiltinManifest(tool)
	if err != nil {
		return "", databaseDownloadAsset{}, err
	}

	return resolveDownloadCatalogAsset(tool, manifest.Download.Catalog, requestedVersion, goos, goarch)
}

func resolveDownloadCatalogAsset(tool string, catalog downloadCatalog, requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	requestedVersion = strings.TrimSpace(requestedVersion)
	if requestedVersion == "" {
		return "", databaseDownloadAsset{}, fmt.Errorf("%s version cannot be empty", tool)
	}
	if len(catalog) == 0 {
		return "", databaseDownloadAsset{}, fmt.Errorf("%s does not define automatic downloads", tool)
	}

	resolvedVersion, err := resolveCatalogVersion(catalog, requestedVersion)
	if err != nil {
		return "", databaseDownloadAsset{}, fmt.Errorf("resolve %s version %q: %w", tool, requestedVersion, err)
	}

	platformAssets := catalog[resolvedVersion]
	for _, key := range downloadPlatformKeys(goos, goarch) {
		if asset, ok := platformAssets[key]; ok {
			return resolvedVersion, asset, nil
		}
	}

	return "", databaseDownloadAsset{}, fmt.Errorf("%s version %q is not available for %s/%s", tool, resolvedVersion, goos, goarch)
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

func resolveDatabaseCatalogVersion(catalog map[string]map[string]databaseDownloadAsset, requested string) (string, error) {
	return resolveCatalogVersion(downloadCatalog(catalog), requested)
}

func resolveCatalogVersion(catalog downloadCatalog, requested string) (string, error) {
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
