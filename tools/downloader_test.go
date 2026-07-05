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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

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

func TestParseChecksumValueAcceptsApacheLoungeFormat(t *testing.T) {
	expected := strings.Repeat("A", 64)
	checksumData := strings.Join([]string{
		"\ufeffChecksums created with GPGHash",
		"",
		"SHA1-Checksum for: httpd-2.4.68-260617-Win64-VS18.zip:",
		strings.Repeat("B", 40),
		"",
		"SHA256-Checksum for: httpd-2.4.68-260617-Win64-VS18.zip:",
		expected,
	}, "\n")

	value, err := parseChecksumValueForAlgorithm(checksumAlgorithmSHA256, checksumData, "httpd-2.4.68-260617-Win64-VS18.zip")
	if err != nil {
		t.Fatalf("parseChecksumValueForAlgorithm(apache lounge) error = %v", err)
	}
	if value != expected {
		t.Fatalf("parseChecksumValueForAlgorithm(apache lounge) = %q, want %q", value, expected)
	}
}

func TestDownloadTextRetriesTransientStatus(t *testing.T) {
	withTemporaryDuration(t, &downloadRetryBaseDelay, 0)

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}

		_, _ = w.Write([]byte("ready"))
	}))
	defer server.Close()

	value, err := downloadText(server.Client(), server.URL, "retry test")
	if err != nil {
		t.Fatalf("downloadText() error = %v", err)
	}
	if value != "ready" {
		t.Fatalf("downloadText() = %q, want ready", value)
	}
	if attempts != 2 {
		t.Fatalf("download attempts = %d, want 2", attempts)
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

func TestResolveApacheDownloadAssetSupportsApacheLoungeSeriesLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Join([]string{
			`<a href="httpd-2.4.67-260101-Win64-VS17.zip">older</a>`,
			`<a href="/download/VS18/binaries/httpd-2.4.68-260610-Win64-VS18.zip">new</a>`,
			`<a href="/download/VS18/binaries/httpd-2.4.68-260617-Win64-VS18.zip">newer-build</a>`,
			`<a href="httpd-2.4.68-260617-Win32-VS18.zip">wrong-arch</a>`,
		}, "\n")))
	}))
	defer server.Close()

	withTemporaryString(t, &apacheLoungeDownloadURL, server.URL)

	resolvedVersion, asset, err := resolveApacheDownloadAsset(server.Client(), "2.4", "windows", "amd64")
	if err != nil {
		t.Fatalf("resolveApacheDownloadAsset(2.4) error = %v", err)
	}
	if resolvedVersion != "2.4.68" {
		t.Fatalf("resolveApacheDownloadAsset(2.4) resolved version = %q, want 2.4.68", resolvedVersion)
	}
	if asset.FileName != "httpd-2.4.68-260617-Win64-VS18.zip" {
		t.Fatalf("resolveApacheDownloadAsset(2.4) file = %q, want newest Apache Lounge build", asset.FileName)
	}
	wantURL := server.URL + "/download/VS18/binaries/httpd-2.4.68-260617-Win64-VS18.zip"
	if asset.URL != wantURL {
		t.Fatalf("resolveApacheDownloadAsset(2.4) URL = %q, want %q", asset.URL, wantURL)
	}
	if asset.ChecksumURL != wantURL+".txt" {
		t.Fatalf("resolveApacheDownloadAsset(2.4) checksum URL = %q, want %q", asset.ChecksumURL, wantURL+".txt")
	}
	if asset.ChecksumAlgorithm != checksumAlgorithmSHA256 {
		t.Fatalf("resolveApacheDownloadAsset(2.4) checksum algorithm = %q, want %q", asset.ChecksumAlgorithm, checksumAlgorithmSHA256)
	}
	if asset.ArchiveFormat != archiveFormatZip {
		t.Fatalf("resolveApacheDownloadAsset(2.4) archive format = %q, want %q", asset.ArchiveFormat, archiveFormatZip)
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

func TestResolveTraefikDownloadAssetSupportsSeriesLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{
				"tag_name":"v3.3.1",
				"assets":[
					{"name":"traefik_v3.3.1_windows_amd64.zip","digest":"sha256:` + strings.Repeat("e", 64) + `"},
					{"name":"traefik_v3.3.1_linux_amd64.tar.gz","digest":"sha256:` + strings.Repeat("f", 64) + `"}
				]
			},
			{"tag_name":"v3.3.0","assets":[]}
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
			version:             "3.3",
			goos:                "windows",
			goarch:              "amd64",
			wantResolvedVersion: "3.3.1",
			wantFileName:        "traefik-3.3.1-windows-amd64.zip",
			wantFormat:          archiveFormatZip,
		},
		{
			name:                "linux",
			version:             "3.3",
			goos:                "linux",
			goarch:              "amd64",
			wantResolvedVersion: "3.3.1",
			wantFileName:        "traefik-3.3.1-linux-amd64.tar.gz",
			wantFormat:          archiveFormatTarGz,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolvedVersion, asset, err := resolveTraefikDownloadAsset(server.Client(), test.version, test.goos, test.goarch)
			if err != nil {
				t.Fatalf("resolveTraefikDownloadAsset(%s) error = %v", test.version, err)
			}
			if resolvedVersion != test.wantResolvedVersion {
				t.Fatalf("resolveTraefikDownloadAsset(%s) resolved version = %q, want %q", test.version, resolvedVersion, test.wantResolvedVersion)
			}
			if asset.FileName != test.wantFileName {
				t.Fatalf("resolveTraefikDownloadAsset(%s) file = %q, want %q", test.version, asset.FileName, test.wantFileName)
			}
			if asset.ArchiveFormat != test.wantFormat {
				t.Fatalf("resolveTraefikDownloadAsset(%s) archive format = %q, want %q", test.version, asset.ArchiveFormat, test.wantFormat)
			}
			if asset.ChecksumAlgorithm != checksumAlgorithmSHA256 || asset.Checksum == "" {
				t.Fatalf("resolveTraefikDownloadAsset(%s) checksum = (%q, %q), want github sha256 digest", test.version, asset.ChecksumAlgorithm, asset.Checksum)
			}
		})
	}
}

func TestResolveMeilisearchDownloadAssetSupportsDirectBinaries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{
				"tag_name":"v1.48.3",
				"assets":[
					{"name":"meilisearch-windows-amd64.exe","digest":"sha256:` + strings.Repeat("a", 64) + `"},
					{"name":"meilisearch-linux-amd64","digest":"sha256:` + strings.Repeat("b", 64) + `"}
				]
			}
		]`))
	}))
	defer server.Close()

	withTemporaryString(t, &githubAPIBaseURL, server.URL)

	tests := []struct {
		name        string
		goos        string
		goarch      string
		wantFile    string
		wantInstall string
	}{
		{name: "windows", goos: "windows", goarch: "amd64", wantFile: "meilisearch-windows-amd64.exe", wantInstall: "meilisearch.exe"},
		{name: "linux", goos: "linux", goarch: "amd64", wantFile: "meilisearch-linux-amd64", wantInstall: "meilisearch"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolvedVersion, asset, err := resolveMeilisearchDownloadAsset(server.Client(), "1.48", test.goos, test.goarch)
			if err != nil {
				t.Fatalf("resolveMeilisearchDownloadAsset() error = %v", err)
			}
			if resolvedVersion != "1.48.3" {
				t.Fatalf("resolved version = %q, want 1.48.3", resolvedVersion)
			}
			if asset.FileName != test.wantFile || asset.InstallPath != test.wantInstall {
				t.Fatalf("asset = %#v, want file %q install %q", asset, test.wantFile, test.wantInstall)
			}
			if asset.ArchiveFormat != "" {
				t.Fatalf("archive format = %q, want direct file", asset.ArchiveFormat)
			}
			if asset.ChecksumAlgorithm != checksumAlgorithmSHA256 || asset.Checksum == "" {
				t.Fatalf("checksum = (%q, %q), want github sha256 digest", asset.ChecksumAlgorithm, asset.Checksum)
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

func TestDownloadRedisResolvesAPTIndexAndExtractsDebianPackages(t *testing.T) {
	serverDeb := buildDebianPackage(t, "usr/bin/redis-server", []byte("redis-server"))
	toolsDeb := buildDebianPackage(t, "usr/bin/redis-cli", []byte("redis-cli"))
	packages := strings.Join([]string{
		strings.Join([]string{
			"Package: redis-server",
			"Version: 6:8.8.0-1rl1~jammy1",
			"Filename: pool/jammy/r/re/redis-server_8.8.0-1rl1~jammy1_amd64.deb",
			"SHA256: " + checksumForBytes(t, checksumAlgorithmSHA256, serverDeb),
		}, "\n"),
		strings.Join([]string{
			"Package: redis-tools",
			"Version: 6:8.8.0-1rl1~jammy1",
			"Filename: pool/jammy/r/re/redis-tools_8.8.0-1rl1~jammy1_amd64.deb",
			"SHA256: " + checksumForBytes(t, checksumAlgorithmSHA256, toolsDeb),
		}, "\n"),
	}, "\n\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dists/jammy/main/binary-amd64/Packages":
			_, _ = w.Write([]byte(packages))
		case "/pool/jammy/r/re/redis-server_8.8.0-1rl1~jammy1_amd64.deb":
			_, _ = w.Write(serverDeb)
		case "/pool/jammy/r/re/redis-tools_8.8.0-1rl1~jammy1_amd64.deb":
			_, _ = w.Write(toolsDeb)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	withTemporaryString(t, &redisAPTBaseURL, server.URL)
	withTemporaryRedisDistributions(t, []string{"jammy"})
	withTemporaryRedisPackagesURL(t, func(distribution string) string {
		return server.URL + "/dists/" + distribution + "/main/binary-amd64/Packages"
	})
	withTemporaryRedisRuntime(t, "linux", "amd64")

	cacheDir := t.TempDir()
	if err := downloadRedis(server.Client(), cacheDir, "8.8"); err != nil {
		t.Fatalf("downloadRedis() error = %v", err)
	}

	payload, err := CachedToolPayload(cacheDir, Redis, "8.8")
	if err != nil {
		t.Fatalf("CachedToolPayload(redis 8.8) error = %v", err)
	}
	if payload.DownloadedVersion != "8.8.0" {
		t.Fatalf("CachedToolPayload(redis).DownloadedVersion = %q, want 8.8.0", payload.DownloadedVersion)
	}
	targetDir := filepath.Join(t.TempDir(), "install")
	if _, err := InstallCachedToolPayload(cacheDir, targetDir, Redis, "8.8"); err != nil {
		t.Fatalf("InstallCachedToolPayload(redis 8.8) error = %v", err)
	}
	for path, want := range map[string]string{
		filepath.Join(targetDir, "bin", "redis-server"): "redis-server",
		filepath.Join(targetDir, "bin", "redis-cli"):    "redis-cli",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", path, err)
		}
		if string(data) != want {
			t.Fatalf("ReadFile(%s) = %q, want %q", path, string(data), want)
		}
	}
}

func TestDownloadRedisWindowsResolvesGitHubReleaseSourceArchive(t *testing.T) {
	archiveData := buildZipArchiveFiles(t, "redis-windows-8.8.0", map[string][]byte{
		"redis-server.exe": []byte("redis-server"),
		"redis-cli.exe":    []byte("redis-cli"),
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/zkteco-home/redis-windows/releases":
			_, _ = w.Write([]byte(`[
				{"tag_name":"8.8.0","draft":false,"prerelease":false,"assets":[]},
				{"tag_name":"8.6.3","draft":false,"prerelease":false,"assets":[]}
			]`))
		case "/archive/refs/tags/8.8.0.zip":
			_, _ = w.Write(archiveData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	withTemporaryString(t, &githubAPIBaseURL, server.URL)
	withTemporaryRedisWindowsArchiveURL(t, func(tag string) string {
		return server.URL + "/archive/refs/tags/" + tag + ".zip"
	})
	withTemporaryRedisRuntime(t, "windows", "amd64")

	cacheDir := t.TempDir()
	if err := downloadRedis(server.Client(), cacheDir, "8.8"); err != nil {
		t.Fatalf("downloadRedis(windows) error = %v", err)
	}

	payload, err := CachedToolPayload(cacheDir, Redis, "8.8")
	if err != nil {
		t.Fatalf("CachedToolPayload(redis 8.8) error = %v", err)
	}
	if payload.DownloadedVersion != "8.8.0" {
		t.Fatalf("CachedToolPayload(redis).DownloadedVersion = %q, want 8.8.0", payload.DownloadedVersion)
	}
	targetDir := filepath.Join(t.TempDir(), "install")
	if _, err := InstallCachedToolPayload(cacheDir, targetDir, Redis, "8.8"); err != nil {
		t.Fatalf("InstallCachedToolPayload(redis 8.8) error = %v", err)
	}
	for path, want := range map[string]string{
		filepath.Join(targetDir, "redis-server.exe"): "redis-server",
		filepath.Join(targetDir, "redis-cli.exe"):    "redis-cli",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", path, err)
		}
		if string(data) != want {
			t.Fatalf("ReadFile(%s) = %q, want %q", path, string(data), want)
		}
	}
}

func TestDownloadRedisRejectsUnsupportedPlatform(t *testing.T) {
	withTemporaryRedisRuntime(t, "darwin", "arm64")

	err := downloadRedis(http.DefaultClient, t.TempDir(), "8.8")
	if err == nil || !strings.Contains(err.Error(), "supported only on linux/amd64 and windows/amd64") {
		t.Fatalf("downloadRedis(unsupported) error = %v, want platform error", err)
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

	return buildZipArchiveFiles(t, rootDir, map[string][]byte{filePath: contents})
}

func buildZipArchiveFiles(t *testing.T, rootDir string, files map[string][]byte) []byte {
	t.Helper()

	buffer := &bytes.Buffer{}
	writer := zip.NewWriter(buffer)
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, filepath.ToSlash(path))
	}
	sort.Strings(paths)
	for _, path := range paths {
		fileWriter, err := writer.Create(filepath.ToSlash(filepath.Join(rootDir, path)))
		if err != nil {
			t.Fatalf("Create(zip entry) error = %v", err)
		}
		if _, err := fileWriter.Write(files[path]); err != nil {
			t.Fatalf("Write(zip entry) error = %v", err)
		}
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

func buildDebianPackage(t *testing.T, filePath string, contents []byte) []byte {
	t.Helper()

	buffer := &bytes.Buffer{}
	buffer.WriteString("!<arch>\n")
	writeArMember(t, buffer, "debian-binary", []byte("2.0\n"))
	writeArMember(t, buffer, "control.tar.xz", buildTarXZArchive(t, ".", "control", []byte("Package: redis\n")))
	writeArMember(t, buffer, "data.tar.xz", buildTarXZArchive(t, ".", filePath, contents))

	return buffer.Bytes()
}

func writeArMember(t *testing.T, buffer *bytes.Buffer, name string, data []byte) {
	t.Helper()

	if len(name) > 15 {
		t.Fatalf("ar member name %q is too long for test helper", name)
	}
	header := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8o%-10d`\n", name+"/", 0, 0, 0, 0o644, len(data))
	buffer.WriteString(header)
	buffer.Write(data)
	if len(data)%2 != 0 {
		buffer.WriteByte('\n')
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

func withTemporaryRedisDistributions(t *testing.T, value []string) {
	t.Helper()

	original := redisAPTDistributions
	redisAPTDistributions = value
	t.Cleanup(func() {
		redisAPTDistributions = original
	})
}

func withTemporaryRedisPackagesURL(t *testing.T, value func(string) string) {
	t.Helper()

	original := redisAPTPackagesURL
	redisAPTPackagesURL = value
	t.Cleanup(func() {
		redisAPTPackagesURL = original
	})
}

func withTemporaryRedisWindowsArchiveURL(t *testing.T, value func(string) string) {
	t.Helper()

	original := redisWindowsArchiveURL
	redisWindowsArchiveURL = value
	t.Cleanup(func() {
		redisWindowsArchiveURL = original
	})
}

func withTemporaryRedisRuntime(t *testing.T, goos, goarch string) {
	t.Helper()

	originalGOOS := redisRuntimeGOOS
	originalGOARCH := redisRuntimeGOARCH
	redisRuntimeGOOS = func() string { return goos }
	redisRuntimeGOARCH = func() string { return goarch }
	t.Cleanup(func() {
		redisRuntimeGOOS = originalGOOS
		redisRuntimeGOARCH = originalGOARCH
	})
}

func withTemporaryDuration(t *testing.T, target *time.Duration, value time.Duration) {
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
