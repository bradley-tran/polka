package tools

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha3"
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

func TestSelectReleaseIndexVersionPrefersStablePatchForMinorLabel(t *testing.T) {
	page := strings.Join([]string{
		`<a href="https://getcomposer.org/download/2.10.0-RC2/composer.phar">rc</a>`,
		`<a href="https://getcomposer.org/download/2.9.8/composer.phar">stable</a>`,
		`<a href="https://getcomposer.org/download/2.8.12/composer.phar">minor-latest</a>`,
		`<a href="https://getcomposer.org/download/2.8.11/composer.phar">minor-older</a>`,
		`<a href="https://getcomposer.org/download/2.2.28/composer.phar">lts</a>`,
	}, "\n")
	pattern := `(?:https://getcomposer\.org)?/download/([0-9]+(?:\.[0-9]+){1,2}(?:-[0-9A-Za-z.-]+)?)/composer\.phar`

	version, err := selectReleaseIndexVersion(page, pattern, "2.8")
	if err != nil {
		t.Fatalf("selectReleaseIndexVersion(2.8) error = %v", err)
	}
	if version != "2.8.12" {
		t.Fatalf("selectReleaseIndexVersion(2.8) = %q, want %q", version, "2.8.12")
	}

	version, err = selectReleaseIndexVersion(page, pattern, "2")
	if err != nil {
		t.Fatalf("selectReleaseIndexVersion(2) error = %v", err)
	}
	if version != "2.9.8" {
		t.Fatalf("selectReleaseIndexVersion(2) = %q, want %q", version, "2.9.8")
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mysql/8.4.html":
			_, _ = w.Write([]byte(strings.Join([]string{
				`<a href="mysql-8.4.8-winx64.zip">mysql</a>`,
				`<a href="mysql-8.4.9-winx64.zip">mysql</a>`,
			}, "\n")))
		case "/mariadb/":
			_, _ = w.Write([]byte(strings.Join([]string{
				`<a href="mariadb-11.4.10/">mariadb-11.4.10/</a>`,
				`<a href="mariadb-11.4.11/">mariadb-11.4.11/</a>`,
				`<a href="mariadb-11.8.7/">mariadb-11.8.7/</a>`,
			}, "\n")))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	withTemporaryString(t, &mysqlDownloadPageURLPattern, server.URL+"/mysql/%s.html")
	withTemporaryString(t, &mariaDBArchiveIndexURL, server.URL+"/mariadb/")

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
			resolvedVersion, asset, err := resolveDatabaseDownloadAsset(server.Client(), test.tool, test.version, test.goos, test.goarch)
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

func TestResolveSQLiteDownloadAssetSupportsSeriesLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Join([]string{
			"PRODUCT,3.53.2,2026/sqlite-tools-linux-x64-3530200.zip,4262741," + strings.Repeat("a", 64),
			"PRODUCT,3.53.2,2026/sqlite-tools-win-x64-3530200.zip,6556897," + strings.Repeat("b", 64),
		}, "\n")))
	}))
	defer server.Close()

	withTemporaryString(t, &sqliteDownloadPageURL, server.URL)

	tests := []struct {
		name                string
		version             string
		goos                string
		goarch              string
		wantResolvedVersion string
		wantFileName        string
	}{
		{
			name:                "windows",
			version:             "3.53",
			goos:                "windows",
			goarch:              "amd64",
			wantResolvedVersion: "3.53.2",
			wantFileName:        "sqlite-tools-win-x64-3530200.zip",
		},
		{
			name:                "linux",
			version:             "3.53",
			goos:                "linux",
			goarch:              "amd64",
			wantResolvedVersion: "3.53.2",
			wantFileName:        "sqlite-tools-linux-x64-3530200.zip",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolvedVersion, asset, err := resolveSQLiteDownloadAsset(server.Client(), test.version, test.goos, test.goarch)
			if err != nil {
				t.Fatalf("resolveSQLiteDownloadAsset(%s) error = %v", test.version, err)
			}
			if resolvedVersion != test.wantResolvedVersion {
				t.Fatalf("resolveSQLiteDownloadAsset(%s) resolved version = %q, want %q", test.version, resolvedVersion, test.wantResolvedVersion)
			}
			if asset.FileName != test.wantFileName {
				t.Fatalf("resolveSQLiteDownloadAsset(%s) file = %q, want %q", test.version, asset.FileName, test.wantFileName)
			}
			if asset.ChecksumAlgorithm != checksumAlgorithmSHA3_256 {
				t.Fatalf("resolveSQLiteDownloadAsset(%s) checksum algorithm = %q, want %q", test.version, asset.ChecksumAlgorithm, checksumAlgorithmSHA3_256)
			}
		})
	}
}

func TestResolveNginxDownloadAssetSupportsSeriesLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Join([]string{
			`<a href="nginx-1.30.1.zip">nginx-1.30.1.zip</a>`,
			`<a href="nginx-1.30.2.zip">nginx-1.30.2.zip</a>`,
			`<a href="nginx-1.28.3.zip">nginx-1.28.3.zip</a>`,
		}, "\n")))
	}))
	defer server.Close()

	withTemporaryString(t, &nginxDownloadIndexURL, server.URL)

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
			resolvedVersion, asset, err := resolveNginxDownloadAsset(server.Client(), test.version, "windows", "amd64")
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"version":"v24.16.0"},
			{"version":"v24.15.0"},
			{"version":"v22.22.3"}
		]`))
	}))
	defer server.Close()

	withTemporaryString(t, &nodeJSReleaseIndexURL, server.URL)

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
			resolvedVersion, asset, err := resolveNodeJSDownloadAsset(server.Client(), test.version, test.goos, test.goarch)
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{
				"tag_name":"v1.30.1",
				"assets":[
					{"name":"mailpit-windows-amd64.zip","digest":"sha256:` + strings.Repeat("c", 64) + `"},
					{"name":"mailpit-linux-amd64.tar.gz","digest":"sha256:` + strings.Repeat("d", 64) + `"}
				]
			},
			{"tag_name":"v1.30.0","assets":[]}
		]`))
	}))
	defer server.Close()

	withTemporaryString(t, &githubAPIBaseURL, server.URL)

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
			wantFileName:        "mailpit-1.30.1-windows-amd64.zip",
			wantFormat:          archiveFormatZip,
		},
		{
			name:                "linux",
			version:             "1.30",
			goos:                "linux",
			goarch:              "amd64",
			wantResolvedVersion: "1.30.1",
			wantFileName:        "mailpit-1.30.1-linux-amd64.tar.gz",
			wantFormat:          archiveFormatTarGz,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolvedVersion, asset, err := resolveMailpitDownloadAsset(server.Client(), test.version, test.goos, test.goarch)
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
			if asset.ChecksumAlgorithm != checksumAlgorithmSHA256 || asset.Checksum == "" {
				t.Fatalf("resolveMailpitDownloadAsset(%s) checksum = (%q, %q), want github sha256 digest", test.version, asset.ChecksumAlgorithm, asset.Checksum)
			}
		})
	}
}

func TestResolveMagoDownloadAssetSupportsSeriesLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{
				"tag_name":"1.27.0",
				"assets":[
					{"name":"mago-1.27.0-x86_64-pc-windows-msvc.zip","digest":"sha256:` + strings.Repeat("e", 64) + `"},
					{"name":"mago-1.27.0-x86_64-unknown-linux-gnu.tar.gz","digest":"sha256:` + strings.Repeat("f", 64) + `"}
				]
			},
			{"tag_name":"1.27.1-RC1","prerelease":true,"assets":[]}
		]`))
	}))
	defer server.Close()

	withTemporaryString(t, &githubAPIBaseURL, server.URL)

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
			version:             "1.27",
			goos:                "windows",
			goarch:              "amd64",
			wantResolvedVersion: "1.27.0",
			wantFileName:        "mago-1.27.0-x86_64-pc-windows-msvc.zip",
			wantFormat:          archiveFormatZip,
		},
		{
			name:                "linux",
			version:             "1.27",
			goos:                "linux",
			goarch:              "amd64",
			wantResolvedVersion: "1.27.0",
			wantFileName:        "mago-1.27.0-x86_64-unknown-linux-gnu.tar.gz",
			wantFormat:          archiveFormatTarGz,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolvedVersion, asset, err := resolveMagoDownloadAsset(server.Client(), test.version, test.goos, test.goarch)
			if err != nil {
				t.Fatalf("resolveMagoDownloadAsset(%s) error = %v", test.version, err)
			}
			if resolvedVersion != test.wantResolvedVersion {
				t.Fatalf("resolveMagoDownloadAsset(%s) resolved version = %q, want %q", test.version, resolvedVersion, test.wantResolvedVersion)
			}
			if asset.FileName != test.wantFileName {
				t.Fatalf("resolveMagoDownloadAsset(%s) file = %q, want %q", test.version, asset.FileName, test.wantFileName)
			}
			if asset.ArchiveFormat != test.wantFormat {
				t.Fatalf("resolveMagoDownloadAsset(%s) archive format = %q, want %q", test.version, asset.ArchiveFormat, test.wantFormat)
			}
			if asset.ChecksumAlgorithm != checksumAlgorithmSHA256 {
				t.Fatalf("resolveMagoDownloadAsset(%s) checksum algorithm = %q, want %q", test.version, asset.ChecksumAlgorithm, checksumAlgorithmSHA256)
			}
		})
	}
}

func TestDownloadManifestFileAssetSupportsSeriesLabelsAndVerifiesDigest(t *testing.T) {
	payload := []byte("manifest file payload\n")
	checksum := checksumForBytes(t, checksumAlgorithmSHA256, payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/php/pie/releases":
			_, _ = w.Write([]byte(`[
				{"tag_name":"1.4.5","assets":[{"name":"pie.phar","digest":"sha256:` + checksum + `"}]},
				{"tag_name":"1.4.6-RC1","prerelease":true,"assets":[{"name":"pie.phar"}]}
			]`))
		case "/downloads/1.4.5/pie.phar":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	withTemporaryString(t, &githubAPIBaseURL, server.URL)

	download := manifestDownload{
		GitHub: manifestGitHubDownload{Owner: "php", Repo: "pie"},
		Assets: map[string]downloadAsset{
			"all": {
				FileName:          "pie.phar",
				URL:               server.URL + "/downloads/{tag}/pie.phar",
				InstallPath:       "bin/pie.phar",
				ChecksumAlgorithm: checksumAlgorithmSHA256,
			},
		},
	}
	cacheDir := t.TempDir()
	if err := downloadManifestAssetForRequest(server.Client(), cacheDir, PIE, "1.4", download, "windows", "amd64"); err != nil {
		t.Fatalf("downloadManifestAssetForRequest() error = %v", err)
	}

	cachedPayload, err := CachedToolPayload(cacheDir, PIE, "1.4")
	if err != nil {
		t.Fatalf("CachedToolPayload(pie 1.4) error = %v", err)
	}
	data, err := os.ReadFile(cachedPayload.PayloadPath)
	if err != nil {
		t.Fatalf("ReadFile(downloaded pie.phar) error = %v", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("downloaded manifest file = %q, want %q", string(data), string(payload))
	}
	metadata, err := readToolCacheMetadata(filepath.Join(cacheDir, PIE), PIE)
	if err != nil {
		t.Fatalf("readToolCacheMetadata(pie) error = %v", err)
	}
	entry := metadata.Versions["1.4"]
	if entry.DownloadedVersion != "1.4.5" || entry.PayloadKind != payloadKindFile || entry.Checksum != checksum {
		t.Fatalf("metadata entry = %#v, want resolved file payload with checksum", entry)
	}
	if entry.InstallPath != "bin/pie.phar" {
		t.Fatalf("metadata install path = %q, want bin/pie.phar", entry.InstallPath)
	}
}

func TestManifestDownloadVersionAllowsRequestedPrerelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"1.4.5","assets":[{"name":"pie.phar"}]},
			{"tag_name":"1.4.6-RC1","prerelease":true,"assets":[{"name":"pie.phar"}]}
		]`))
	}))
	defer server.Close()

	withTemporaryString(t, &githubAPIBaseURL, server.URL)

	download := manifestDownload{GitHub: manifestGitHubDownload{Owner: "php", Repo: "pie"}}
	resolvedVersion, tag, _, err := resolveManifestDownloadVersion(server.Client(), PIE, "1.4.6-RC1", download)
	if err != nil {
		t.Fatalf("resolveManifestDownloadVersion() error = %v", err)
	}
	if resolvedVersion != "1.4.6-RC1" || tag != "1.4.6-RC1" {
		t.Fatalf("resolveManifestDownloadVersion() = (%q, %q), want prerelease", resolvedVersion, tag)
	}
}

func TestResolvePHPMyAdminDownloadAssetSupportsSeriesLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Join([]string{
			`<a href="phpMyAdmin-5.2.2-all-languages.zip">old</a>`,
			`<a href="phpMyAdmin-5.2.3-all-languages.zip">new</a>`,
		}, "\n")))
	}))
	defer server.Close()

	withTemporaryString(t, &phpMyAdminDownloadsURL, server.URL)

	resolvedVersion, asset, err := resolvePHPMyAdminDownloadAsset(server.Client(), "5.2")
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

func TestDownloadDatabaseAssetCachesSupportedArchives(t *testing.T) {
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

			payload, err := CachedToolPayload(cacheDir, MySQL, test.requestedVersion)
			if err != nil {
				t.Fatalf("CachedToolPayload(%s) error = %v", test.requestedVersion, err)
			}
			if payload.PayloadKind != string(payloadKindArchive) {
				t.Fatalf("CachedToolPayload(%s).PayloadKind = %q, want archive", test.requestedVersion, payload.PayloadKind)
			}
			if _, err := os.Stat(filepath.Join(cacheDir, MySQL, test.requestedVersion, test.expectedPath)); !os.IsNotExist(err) {
				t.Fatalf("legacy extracted cache path error = %v, want missing", err)
			}

			targetDir := filepath.Join(t.TempDir(), "install")
			if _, err := InstallCachedToolPayload(cacheDir, targetDir, MySQL, test.requestedVersion); err != nil {
				t.Fatalf("InstallCachedToolPayload(%s) error = %v", test.fileName, err)
			}
			installedPath := filepath.Join(targetDir, test.expectedPath)
			if _, err := os.Stat(installedPath); err != nil {
				t.Fatalf("Stat(%s) error = %v", installedPath, err)
			}
		})
	}
}

func TestDownloadDatabaseAssetMetadataTracksMultipleVersions(t *testing.T) {
	archiveData := buildZipArchive(t, "mysql-test", "bin/mysql.exe", []byte("mysql"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archiveData)
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	for _, version := range []string{"8.3", "8.4"} {
		asset := databaseDownloadAsset{
			FileName:          "mysql-" + version + ".zip",
			URL:               server.URL + "/mysql-" + version + ".zip",
			Checksum:          checksumForBytes(t, checksumAlgorithmSHA256, archiveData),
			ChecksumAlgorithm: checksumAlgorithmSHA256,
			ArchiveFormat:     archiveFormatZip,
		}
		if err := downloadDatabaseAsset(server.Client(), cacheDir, MySQL, version, asset); err != nil {
			t.Fatalf("downloadDatabaseAsset(%s) error = %v", version, err)
		}
	}

	metadata, err := readToolCacheMetadata(filepath.Join(cacheDir, MySQL), MySQL)
	if err != nil {
		t.Fatalf("readToolCacheMetadata(mysql) error = %v", err)
	}
	for _, version := range []string{"8.3", "8.4"} {
		entry, ok := metadata.Versions[version]
		if !ok {
			t.Fatalf("metadata versions = %#v, want %s entry", metadata.Versions, version)
		}
		if entry.DownloadedVersion != version || entry.PayloadKind != payloadKindArchive || entry.ArchiveFormat != archiveFormatZip {
			t.Fatalf("metadata entry %s = %#v, want archive metadata", version, entry)
		}
		if _, err := os.Stat(filepath.Join(cacheDir, MySQL, filepath.FromSlash(entry.PayloadPath))); err != nil {
			t.Fatalf("Stat(payload %s) error = %v", version, err)
		}
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

func withTemporaryString(t *testing.T, target *string, value string) {
	t.Helper()

	original := *target
	*target = value
	t.Cleanup(func() {
		*target = original
	})
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
	case checksumAlgorithmSHA3_256:
		sum := sha3.Sum256(data)
		return hex.EncodeToString(sum[:])
	default:
		t.Fatalf("unsupported checksum algorithm %q", algorithm)
		return ""
	}
}
