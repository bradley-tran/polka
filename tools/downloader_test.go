package tools

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

func TestSelectComposerReleaseVersionPrefersStablePatchForMinorLabel(t *testing.T) {
	page := strings.Join([]string{
		`<a href="https://getcomposer.org/download/2.10.0-RC2/composer.phar">rc</a>`,
		`<a href="https://getcomposer.org/download/2.9.8/composer.phar">stable</a>`,
		`<a href="https://getcomposer.org/download/2.8.12/composer.phar">minor-latest</a>`,
		`<a href="https://getcomposer.org/download/2.8.11/composer.phar">minor-older</a>`,
		`<a href="https://getcomposer.org/download/2.2.28/composer.phar">lts</a>`,
	}, "\n")

	version, err := selectComposerReleaseVersion(page, "2.8")
	if err != nil {
		t.Fatalf("selectComposerReleaseVersion(2.8) error = %v", err)
	}
	if version != "2.8.12" {
		t.Fatalf("selectComposerReleaseVersion(2.8) = %q, want %q", version, "2.8.12")
	}

	version, err = selectComposerReleaseVersion(page, "2")
	if err != nil {
		t.Fatalf("selectComposerReleaseVersion(2) error = %v", err)
	}
	if version != "2.9.8" {
		t.Fatalf("selectComposerReleaseVersion(2) = %q, want %q", version, "2.9.8")
	}
}

func TestParseChecksumValueAcceptsSha256sumFormat(t *testing.T) {
	expected := strings.Repeat("a", 64)
	value, err := parseChecksumValue(expected + "  composer.phar\n")
	if err != nil {
		t.Fatalf("parseChecksumValue(sha256sum) error = %v", err)
	}
	if value != expected {
		t.Fatalf("parseChecksumValue(sha256sum) = %q, want %q", value, expected)
	}
}

func TestResolveDatabaseDownloadAssetSupportsSeriesLabels(t *testing.T) {
	tests := []struct {
		name                string
		tool                string
		version             string
		goos                string
		goarch              string
		wantResolvedVersion string
		wantFileName        string
		wantAlgorithm       checksumAlgorithm
	}{
		{
			name:                "mysql windows",
			tool:                MySQL,
			version:             "8.4",
			goos:                "windows",
			goarch:              "amd64",
			wantResolvedVersion: "8.4.9",
			wantFileName:        "mysql-8.4.9-winx64.zip",
			wantAlgorithm:       checksumAlgorithmMD5,
		},
		{
			name:                "mysql linux",
			tool:                MySQL,
			version:             "8.4",
			goos:                "linux",
			goarch:              "amd64",
			wantResolvedVersion: "8.4.9",
			wantFileName:        "mysql-8.4.9-linux-glibc2.17-x86_64.tar.xz",
			wantAlgorithm:       checksumAlgorithmMD5,
		},
		{
			name:                "mariadb windows",
			tool:                MariaDB,
			version:             "11.4",
			goos:                "windows",
			goarch:              "amd64",
			wantResolvedVersion: "11.4.11",
			wantFileName:        "mariadb-11.4.11-winx64.zip",
			wantAlgorithm:       checksumAlgorithmSHA256,
		},
		{
			name:                "mariadb linux",
			tool:                MariaDB,
			version:             "11.4",
			goos:                "linux",
			goarch:              "amd64",
			wantResolvedVersion: "11.4.11",
			wantFileName:        "mariadb-11.4.11-linux-systemd-x86_64.tar.gz",
			wantAlgorithm:       checksumAlgorithmSHA256,
		},
		{
			name:                "mariadb 11.8 windows",
			tool:                MariaDB,
			version:             "11.8",
			goos:                "windows",
			goarch:              "amd64",
			wantResolvedVersion: "11.8.7",
			wantFileName:        "mariadb-11.8.7-winx64.zip",
			wantAlgorithm:       checksumAlgorithmSHA256,
		},
		{
			name:                "mariadb 11.8 linux",
			tool:                MariaDB,
			version:             "11.8",
			goos:                "linux",
			goarch:              "amd64",
			wantResolvedVersion: "11.8.7",
			wantFileName:        "mariadb-11.8.7-linux-systemd-x86_64.tar.gz",
			wantAlgorithm:       checksumAlgorithmSHA256,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolvedVersion, asset, err := resolveDatabaseDownloadAsset(test.tool, test.version, test.goos, test.goarch)
			if err != nil {
				t.Fatalf("resolveDatabaseDownloadAsset(%s, %s) error = %v", test.tool, test.version, err)
			}
			if resolvedVersion != test.wantResolvedVersion {
				t.Fatalf("resolveDatabaseDownloadAsset(%s, %s) resolved version = %q, want %q", test.tool, test.version, resolvedVersion, test.wantResolvedVersion)
			}
			if asset.FileName != test.wantFileName {
				t.Fatalf("resolveDatabaseDownloadAsset(%s, %s) file = %q, want %q", test.tool, test.version, asset.FileName, test.wantFileName)
			}
			if asset.ChecksumAlgorithm != test.wantAlgorithm {
				t.Fatalf("resolveDatabaseDownloadAsset(%s, %s) checksum algorithm = %q, want %q", test.tool, test.version, asset.ChecksumAlgorithm, test.wantAlgorithm)
			}
		})
	}
}

func TestResolveNginxDownloadAssetSupportsSeriesLabels(t *testing.T) {
	tests := []struct {
		name                string
		version             string
		wantResolvedVersion string
		wantFileName        string
	}{
		{
			name:                "stable series",
			version:             "1.30",
			wantResolvedVersion: "1.30.2",
			wantFileName:        "nginx-1.30.2.zip",
		},
		{
			name:                "legacy series",
			version:             "1.28",
			wantResolvedVersion: "1.28.3",
			wantFileName:        "nginx-1.28.3.zip",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolvedVersion, asset, err := resolveNginxDownloadAsset(test.version, "windows", "amd64")
			if err != nil {
				t.Fatalf("resolveNginxDownloadAsset(%s) error = %v", test.version, err)
			}
			if resolvedVersion != test.wantResolvedVersion {
				t.Fatalf("resolveNginxDownloadAsset(%s) resolved version = %q, want %q", test.version, resolvedVersion, test.wantResolvedVersion)
			}
			if asset.FileName != test.wantFileName {
				t.Fatalf("resolveNginxDownloadAsset(%s) file = %q, want %q", test.version, asset.FileName, test.wantFileName)
			}
			if asset.ChecksumAlgorithm != checksumAlgorithmNone {
				t.Fatalf("resolveNginxDownloadAsset(%s) checksum algorithm = %q, want none", test.version, asset.ChecksumAlgorithm)
			}
		})
	}
}

func TestResolveNodeJSDownloadAssetSupportsSeriesLabels(t *testing.T) {
	tests := []struct {
		name                string
		version             string
		goos                string
		goarch              string
		wantResolvedVersion string
		wantFileName        string
	}{
		{
			name:                "current lts windows",
			version:             "24",
			goos:                "windows",
			goarch:              "amd64",
			wantResolvedVersion: "24.16.0",
			wantFileName:        "node-v24.16.0-win-x64.zip",
		},
		{
			name:                "current lts linux",
			version:             "24",
			goos:                "linux",
			goarch:              "amd64",
			wantResolvedVersion: "24.16.0",
			wantFileName:        "node-v24.16.0-linux-x64.tar.xz",
		},
		{
			name:                "previous lts windows",
			version:             "22",
			goos:                "windows",
			goarch:              "amd64",
			wantResolvedVersion: "22.22.3",
			wantFileName:        "node-v22.22.3-win-x64.zip",
		},
		{
			name:                "previous lts linux",
			version:             "22",
			goos:                "linux",
			goarch:              "amd64",
			wantResolvedVersion: "22.22.3",
			wantFileName:        "node-v22.22.3-linux-x64.tar.xz",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolvedVersion, asset, err := resolveNodeJSDownloadAsset(test.version, test.goos, test.goarch)
			if err != nil {
				t.Fatalf("resolveNodeJSDownloadAsset(%s) error = %v", test.version, err)
			}
			if resolvedVersion != test.wantResolvedVersion {
				t.Fatalf("resolveNodeJSDownloadAsset(%s) resolved version = %q, want %q", test.version, resolvedVersion, test.wantResolvedVersion)
			}
			if asset.FileName != test.wantFileName {
				t.Fatalf("resolveNodeJSDownloadAsset(%s) file = %q, want %q", test.version, asset.FileName, test.wantFileName)
			}
			if asset.ChecksumAlgorithm != checksumAlgorithmSHA256 {
				t.Fatalf("resolveNodeJSDownloadAsset(%s) checksum algorithm = %q, want %q", test.version, asset.ChecksumAlgorithm, checksumAlgorithmSHA256)
			}
		})
	}
}

func TestResolveMailpitDownloadAssetSupportsSeriesLabels(t *testing.T) {
	tests := []struct {
		name                string
		version             string
		goos                string
		goarch              string
		wantResolvedVersion string
		wantFileName        string
		wantFormat          archiveFormat
	}{
		{
			name:                "windows",
			version:             "1.30",
			goos:                "windows",
			goarch:              "amd64",
			wantResolvedVersion: "1.30.1",
			wantFileName:        "mailpit-windows-amd64.zip",
			wantFormat:          archiveFormatZip,
		},
		{
			name:                "linux",
			version:             "1.30",
			goos:                "linux",
			goarch:              "amd64",
			wantResolvedVersion: "1.30.1",
			wantFileName:        "mailpit-linux-amd64.tar.gz",
			wantFormat:          archiveFormatTarGz,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolvedVersion, asset, err := resolveMailpitDownloadAsset(test.version, test.goos, test.goarch)
			if err != nil {
				t.Fatalf("resolveMailpitDownloadAsset(%s) error = %v", test.version, err)
			}
			if resolvedVersion != test.wantResolvedVersion {
				t.Fatalf("resolveMailpitDownloadAsset(%s) resolved version = %q, want %q", test.version, resolvedVersion, test.wantResolvedVersion)
			}
			if asset.FileName != test.wantFileName {
				t.Fatalf("resolveMailpitDownloadAsset(%s) file = %q, want %q", test.version, asset.FileName, test.wantFileName)
			}
			if asset.ArchiveFormat != test.wantFormat {
				t.Fatalf("resolveMailpitDownloadAsset(%s) archive format = %q, want %q", test.version, asset.ArchiveFormat, test.wantFormat)
			}
			if asset.ChecksumAlgorithm != checksumAlgorithmNone {
				t.Fatalf("resolveMailpitDownloadAsset(%s) checksum algorithm = %q, want none", test.version, asset.ChecksumAlgorithm)
			}
		})
	}
}

func TestResolvePHPMyAdminDownloadAssetSupportsSeriesLabels(t *testing.T) {
	resolvedVersion, asset, err := resolvePHPMyAdminDownloadAsset("5.2")
	if err != nil {
		t.Fatalf("resolvePHPMyAdminDownloadAsset(5.2) error = %v", err)
	}
	if resolvedVersion != "5.2.3" {
		t.Fatalf("resolvePHPMyAdminDownloadAsset(5.2) resolved version = %q, want %q", resolvedVersion, "5.2.3")
	}
	if asset.FileName != "phpMyAdmin-5.2.3-all-languages.zip" {
		t.Fatalf("resolvePHPMyAdminDownloadAsset(5.2) file = %q, want phpMyAdmin archive", asset.FileName)
	}
	if asset.ArchiveFormat != archiveFormatZip {
		t.Fatalf("resolvePHPMyAdminDownloadAsset(5.2) archive format = %q, want %q", asset.ArchiveFormat, archiveFormatZip)
	}
	if asset.ChecksumAlgorithm != checksumAlgorithmSHA256 {
		t.Fatalf("resolvePHPMyAdminDownloadAsset(5.2) checksum algorithm = %q, want %q", asset.ChecksumAlgorithm, checksumAlgorithmSHA256)
	}
}

func TestDownloadDatabaseAssetExtractsSupportedArchives(t *testing.T) {
	tests := []struct {
		name             string
		requestedVersion string
		fileName         string
		format           archiveFormat
		algorithm        checksumAlgorithm
		archiveData      func(t *testing.T) []byte
		expectedPath     string
	}{
		{
			name:             "zip md5",
			requestedVersion: "8.4",
			fileName:         "mysql-8.4.9-winx64.zip",
			format:           archiveFormatZip,
			algorithm:        checksumAlgorithmMD5,
			archiveData: func(t *testing.T) []byte {
				return buildZipArchive(t, "mysql-8.4.9-winx64", "bin/mysql.exe", []byte("mysql"))
			},
			expectedPath: filepath.Join("bin", "mysql.exe"),
		},
		{
			name:             "tar.gz sha256",
			requestedVersion: "11.4",
			fileName:         "mariadb-11.4.11-linux-systemd-x86_64.tar.gz",
			format:           archiveFormatTarGz,
			algorithm:        checksumAlgorithmSHA256,
			archiveData: func(t *testing.T) []byte {
				return buildTarGzipArchive(t, "mariadb-11.4.11-linux-systemd-x86_64", "bin/mariadb", []byte("mariadb"))
			},
			expectedPath: filepath.Join("bin", "mariadb"),
		},
		{
			name:             "tar.xz md5",
			requestedVersion: "8.4",
			fileName:         "mysql-8.4.9-linux-glibc2.17-x86_64.tar.xz",
			format:           archiveFormatTarXz,
			algorithm:        checksumAlgorithmMD5,
			archiveData: func(t *testing.T) []byte {
				return buildTarXZArchive(t, "mysql-8.4.9-linux-glibc2.17-x86_64", "bin/mysql", []byte("mysql"))
			},
			expectedPath: filepath.Join("bin", "mysql"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archiveData := test.archiveData(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(archiveData)
			}))
			defer server.Close()

			cacheDir := t.TempDir()
			asset := databaseDownloadAsset{
				FileName:          test.fileName,
				URL:               server.URL + "/" + test.fileName,
				Checksum:          checksumForBytes(t, test.algorithm, archiveData),
				ChecksumAlgorithm: test.algorithm,
				ArchiveFormat:     test.format,
			}

			if err := downloadDatabaseAsset(server.Client(), cacheDir, MySQL, test.requestedVersion, asset); err != nil {
				t.Fatalf("downloadDatabaseAsset(%s) error = %v", test.fileName, err)
			}

			installedPath := filepath.Join(cacheDir, MySQL, test.requestedVersion, test.expectedPath)
			if _, err := os.Stat(installedPath); err != nil {
				t.Fatalf("Stat(%s) error = %v", installedPath, err)
			}
		})
	}
}

func buildZipArchive(t *testing.T, rootDir, filePath string, contents []byte) []byte {
	t.Helper()

	buffer := &bytes.Buffer{}
	writer := zip.NewWriter(buffer)
	fileWriter, err := writer.Create(filepath.ToSlash(filepath.Join(rootDir, filePath)))
	if err != nil {
		t.Fatalf("Create(zip entry) error = %v", err)
	}
	if _, err := fileWriter.Write(contents); err != nil {
		t.Fatalf("Write(zip entry) error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close(zip writer) error = %v", err)
	}

	return buffer.Bytes()
}

func buildTarGzipArchive(t *testing.T, rootDir, filePath string, contents []byte) []byte {
	t.Helper()

	buffer := &bytes.Buffer{}
	gzipWriter := gzip.NewWriter(buffer)
	writeTarArchive(t, gzipWriter, rootDir, filePath, contents)
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("Close(gzip writer) error = %v", err)
	}

	return buffer.Bytes()
}

func buildTarXZArchive(t *testing.T, rootDir, filePath string, contents []byte) []byte {
	t.Helper()

	buffer := &bytes.Buffer{}
	xzWriter, err := xz.NewWriter(buffer)
	if err != nil {
		t.Fatalf("NewWriter(xz) error = %v", err)
	}
	writeTarArchive(t, xzWriter, rootDir, filePath, contents)
	if err := xzWriter.Close(); err != nil {
		t.Fatalf("Close(xz writer) error = %v", err)
	}

	return buffer.Bytes()
}

func writeTarArchive(t *testing.T, writer io.Writer, rootDir, filePath string, contents []byte) {
	t.Helper()

	tarWriter := tar.NewWriter(writer)
	fullPath := filepath.ToSlash(filepath.Join(rootDir, filePath))
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: fullPath,
		Mode: 0o755,
		Size: int64(len(contents)),
	}); err != nil {
		t.Fatalf("WriteHeader(tar) error = %v", err)
	}
	if _, err := tarWriter.Write(contents); err != nil {
		t.Fatalf("Write(tar) error = %v", err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("Close(tar writer) error = %v", err)
	}
}

func checksumForBytes(t *testing.T, algorithm checksumAlgorithm, data []byte) string {
	t.Helper()

	switch algorithm {
	case checksumAlgorithmMD5:
		sum := md5.Sum(data)
		return hex.EncodeToString(sum[:])
	case checksumAlgorithmSHA256:
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	default:
		t.Fatalf("unsupported checksum algorithm %q", algorithm)
		return ""
	}
}
