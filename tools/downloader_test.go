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

// loadManifestWithReleaseIndexURL loads a built-in tool manifest and overrides
// its release-index URL for offline httptest verification.
func loadManifestWithReleaseIndexURL(t *testing.T, tool, indexURL string) pluginManifest {
	t.Helper()
	manifest, err := loadBuiltinManifest(tool)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(%s) error = %v", tool, err)
	}
	manifest.Download.ReleaseIndex.URL = indexURL
	return manifest
}

// loadManifestWithScriptVar loads a built-in manifest and overrides one
// script variable, typically to point a resolve hook at a test server.
func loadManifestWithScriptVar(t *testing.T, tool, key, value string) pluginManifest {
	t.Helper()
	manifest, err := loadBuiltinManifest(tool)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(%s) error = %v", tool, err)
	}
	vars := make(map[string]string, len(manifest.Script.Vars)+1)
	for name, current := range manifest.Script.Vars {
		vars[name] = current
	}
	vars[key] = value
	manifest.Script.Vars = vars
	return manifest
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

	manifests := map[string]pluginManifest{
		MySQL:   loadManifestWithReleaseIndexURL(t, MySQL, server.URL+"/mysql/{requested}.html"),
		MariaDB: loadManifestWithReleaseIndexURL(t, MariaDB, server.URL+"/mariadb/"),
	}

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
			plan, err := resolveManifestRequest(server.Client(), manifests[test.tool], test.version, test.goos, test.goarch)
			if err != nil {
				t.Fatalf("resolveManifestRequest(%s, %s) error = %v", test.tool, test.version, err)
			}
			if plan.ResolvedVersion != test.wantResolvedVersion {
				t.Fatalf("resolveManifestRequest(%s, %s) resolved version = %q, want %q", test.tool, test.version, plan.ResolvedVersion, test.wantResolvedVersion)
			}
			if plan.Asset.FileName != test.wantFileName {
				t.Fatalf("resolveManifestRequest(%s, %s) file = %q, want %q", test.tool, test.version, plan.Asset.FileName, test.wantFileName)
			}
			if plan.Asset.ChecksumAlgorithm != test.wantAlgorithm {
				t.Fatalf("resolveManifestRequest(%s, %s) checksum algorithm = %q, want %q", test.tool, test.version, plan.Asset.ChecksumAlgorithm, test.wantAlgorithm)
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

	manifest := loadManifestWithScriptVar(t, SQLite, "download-page", server.URL)

	tests := []struct {
		name                string
		version             string
		goos                string
		goarch              string
		wantResolvedVersion string
		wantFileName        string
		wantChecksum        string
	}{
		{
			name:                "windows",
			version:             "3.53",
			goos:                "windows",
			goarch:              "amd64",
			wantResolvedVersion: "3.53.2",
			wantFileName:        "sqlite-tools-win-x64-3530200.zip",
			wantChecksum:        strings.Repeat("b", 64),
		},
		{
			name:                "linux",
			version:             "3.53",
			goos:                "linux",
			goarch:              "amd64",
			wantResolvedVersion: "3.53.2",
			wantFileName:        "sqlite-tools-linux-x64-3530200.zip",
			wantChecksum:        strings.Repeat("a", 64),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := resolveManifestRequest(server.Client(), manifest, test.version, test.goos, test.goarch)
			if err != nil {
				t.Fatalf("resolveManifestRequest(sqlite, %s) error = %v", test.version, err)
			}
			resolvedVersion, asset := plan.ResolvedVersion, plan.Asset
			if resolvedVersion != test.wantResolvedVersion {
				t.Fatalf("resolveManifestRequest(sqlite, %s) resolved version = %q, want %q", test.version, resolvedVersion, test.wantResolvedVersion)
			}
			if asset.FileName != test.wantFileName {
				t.Fatalf("resolveManifestRequest(sqlite, %s) file = %q, want %q", test.version, asset.FileName, test.wantFileName)
			}
			if asset.ChecksumAlgorithm != checksumAlgorithmSHA3_256 {
				t.Fatalf("resolveManifestRequest(sqlite, %s) checksum algorithm = %q, want %q", test.version, asset.ChecksumAlgorithm, checksumAlgorithmSHA3_256)
			}
			if asset.Checksum != test.wantChecksum {
				t.Fatalf("resolveManifestRequest(sqlite, %s) checksum = %q, want %q", test.version, asset.Checksum, test.wantChecksum)
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

	manifest := loadManifestWithReleaseIndexURL(t, Nginx, server.URL)

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
			plan, err := resolveManifestRequest(server.Client(), manifest, test.version, "windows", "amd64")
			if err != nil {
				t.Fatalf("resolveManifestRequest(%s) error = %v", test.version, err)
			}
			if plan.ResolvedVersion != test.wantResolvedVersion {
				t.Fatalf("resolveManifestRequest(%s) resolved version = %q, want %q", test.version, plan.ResolvedVersion, test.wantResolvedVersion)
			}
			if plan.Asset.FileName != test.wantFileName {
				t.Fatalf("resolveManifestRequest(%s) file = %q, want %q", test.version, plan.Asset.FileName, test.wantFileName)
			}
			if plan.Asset.ChecksumAlgorithm != checksumAlgorithmNone {
				t.Fatalf("resolveManifestRequest(%s) checksum algorithm = %q, want none", test.version, plan.Asset.ChecksumAlgorithm)
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

	manifest := loadManifestWithScriptVar(t, Apache, "download-page", server.URL)

	plan, err := resolveManifestRequest(server.Client(), manifest, "2.4", "windows", "amd64")
	if err != nil {
		t.Fatalf("resolveManifestRequest(apache, 2.4) error = %v", err)
	}
	resolvedVersion, asset := plan.ResolvedVersion, plan.Asset
	if resolvedVersion != "2.4.68" {
		t.Fatalf("resolveManifestRequest(apache, 2.4) resolved version = %q, want 2.4.68", resolvedVersion)
	}
	if asset.FileName != "httpd-2.4.68-260617-Win64-VS18.zip" {
		t.Fatalf("resolveManifestRequest(apache, 2.4) file = %q, want newest Apache Lounge build", asset.FileName)
	}
	wantURL := server.URL + "/download/VS18/binaries/httpd-2.4.68-260617-Win64-VS18.zip"
	if asset.URL != wantURL {
		t.Fatalf("resolveManifestRequest(apache, 2.4) URL = %q, want %q", asset.URL, wantURL)
	}
	if asset.ChecksumURL != wantURL+".txt" {
		t.Fatalf("resolveManifestRequest(apache, 2.4) checksum URL = %q, want %q", asset.ChecksumURL, wantURL+".txt")
	}
	if asset.ChecksumAlgorithm != checksumAlgorithmSHA256 {
		t.Fatalf("resolveManifestRequest(apache, 2.4) checksum algorithm = %q, want %q", asset.ChecksumAlgorithm, checksumAlgorithmSHA256)
	}
	if asset.ArchiveFormat != archiveFormatZip {
		t.Fatalf("resolveManifestRequest(apache, 2.4) archive format = %q, want %q", asset.ArchiveFormat, archiveFormatZip)
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

	manifest := loadManifestWithReleaseIndexURL(t, NodeJS, server.URL)

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
		{
			name:                "prefixed exact version",
			version:             "v22.22.3",
			goos:                "windows",
			goarch:              "amd64",
			wantResolvedVersion: "22.22.3",
			wantFileName:        "node-v22.22.3-win-x64.zip",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := resolveManifestRequest(server.Client(), manifest, test.version, test.goos, test.goarch)
			if err != nil {
				t.Fatalf("resolveManifestRequest(%s) error = %v", test.version, err)
			}
			if plan.ResolvedVersion != test.wantResolvedVersion {
				t.Fatalf("resolveManifestRequest(%s) resolved version = %q, want %q", test.version, plan.ResolvedVersion, test.wantResolvedVersion)
			}
			if plan.Asset.FileName != test.wantFileName {
				t.Fatalf("resolveManifestRequest(%s) file = %q, want %q", test.version, plan.Asset.FileName, test.wantFileName)
			}
			if plan.Asset.ChecksumAlgorithm != checksumAlgorithmSHA256 {
				t.Fatalf("resolveManifestRequest(%s) checksum algorithm = %q, want %q", test.version, plan.Asset.ChecksumAlgorithm, checksumAlgorithmSHA256)
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
	manifest, err := loadBuiltinManifest(Mailpit)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(mailpit) error = %v", err)
	}

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
			plan, err := resolveManifestRequest(server.Client(), manifest, test.version, test.goos, test.goarch)
			if err != nil {
				t.Fatalf("resolveManifestRequest(%s) error = %v", test.version, err)
			}
			if plan.ResolvedVersion != test.wantResolvedVersion {
				t.Fatalf("resolveManifestRequest(%s) resolved version = %q, want %q", test.version, plan.ResolvedVersion, test.wantResolvedVersion)
			}
			if plan.Asset.FileName != test.wantFileName {
				t.Fatalf("resolveManifestRequest(%s) file = %q, want %q", test.version, plan.Asset.FileName, test.wantFileName)
			}
			if plan.Asset.ArchiveFormat != test.wantFormat {
				t.Fatalf("resolveManifestRequest(%s) archive format = %q, want %q", test.version, plan.Asset.ArchiveFormat, test.wantFormat)
			}
			if plan.Asset.ChecksumAlgorithm != checksumAlgorithmSHA256 || plan.Asset.Checksum == "" {
				t.Fatalf("resolveManifestRequest(%s) checksum = (%q, %q), want github sha256 digest", test.version, plan.Asset.ChecksumAlgorithm, plan.Asset.Checksum)
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
	manifest, err := loadBuiltinManifest(Traefik)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(traefik) error = %v", err)
	}

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
			plan, err := resolveManifestRequest(server.Client(), manifest, test.version, test.goos, test.goarch)
			if err != nil {
				t.Fatalf("resolveManifestRequest(%s) error = %v", test.version, err)
			}
			if plan.ResolvedVersion != test.wantResolvedVersion {
				t.Fatalf("resolveManifestRequest(%s) resolved version = %q, want %q", test.version, plan.ResolvedVersion, test.wantResolvedVersion)
			}
			if plan.Asset.FileName != test.wantFileName {
				t.Fatalf("resolveManifestRequest(%s) file = %q, want %q", test.version, plan.Asset.FileName, test.wantFileName)
			}
			if plan.Asset.ArchiveFormat != test.wantFormat {
				t.Fatalf("resolveManifestRequest(%s) archive format = %q, want %q", test.version, plan.Asset.ArchiveFormat, test.wantFormat)
			}
			if plan.Asset.ChecksumAlgorithm != checksumAlgorithmSHA256 || plan.Asset.Checksum == "" {
				t.Fatalf("resolveManifestRequest(%s) checksum = (%q, %q), want github sha256 digest", test.version, plan.Asset.ChecksumAlgorithm, plan.Asset.Checksum)
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
	manifest, err := loadBuiltinManifest(Meilisearch)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(meilisearch) error = %v", err)
	}

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
			plan, err := resolveManifestRequest(server.Client(), manifest, "1.48", test.goos, test.goarch)
			if err != nil {
				t.Fatalf("resolveManifestRequest() error = %v", err)
			}
			if plan.ResolvedVersion != "1.48.3" {
				t.Fatalf("resolved version = %q, want 1.48.3", plan.ResolvedVersion)
			}
			if plan.Asset.FileName != test.wantFile || plan.Asset.InstallPath != test.wantInstall {
				t.Fatalf("asset = %#v, want file %q install %q", plan.Asset, test.wantFile, test.wantInstall)
			}
			if plan.Asset.ArchiveFormat != "" {
				t.Fatalf("archive format = %q, want direct file", plan.Asset.ArchiveFormat)
			}
			if plan.Asset.ChecksumAlgorithm != checksumAlgorithmSHA256 || plan.Asset.Checksum == "" {
				t.Fatalf("checksum = (%q, %q), want github sha256 digest", plan.Asset.ChecksumAlgorithm, plan.Asset.Checksum)
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
	manifest, err := loadBuiltinManifest(Mago)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(mago) error = %v", err)
	}

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
			plan, err := resolveManifestRequest(server.Client(), manifest, test.version, test.goos, test.goarch)
			if err != nil {
				t.Fatalf("resolveManifestRequest(%s) error = %v", test.version, err)
			}
			if plan.ResolvedVersion != test.wantResolvedVersion {
				t.Fatalf("resolveManifestRequest(%s) resolved version = %q, want %q", test.version, plan.ResolvedVersion, test.wantResolvedVersion)
			}
			if plan.Asset.FileName != test.wantFileName {
				t.Fatalf("resolveManifestRequest(%s) file = %q, want %q", test.version, plan.Asset.FileName, test.wantFileName)
			}
			if plan.Asset.ArchiveFormat != test.wantFormat {
				t.Fatalf("resolveManifestRequest(%s) archive format = %q, want %q", test.version, plan.Asset.ArchiveFormat, test.wantFormat)
			}
			if plan.Asset.ChecksumAlgorithm != checksumAlgorithmSHA256 {
				t.Fatalf("resolveManifestRequest(%s) checksum algorithm = %q, want %q", test.version, plan.Asset.ChecksumAlgorithm, checksumAlgorithmSHA256)
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

	manifest := pluginManifest{
		ID: PIE,
		Download: manifestDownload{
			GitHub: manifestGitHubDownload{Owner: "php", Repo: "pie"},
			Assets: map[string]downloadAsset{
				"all": {
					FileName:          "pie.phar",
					URL:               server.URL + "/downloads/{tag}/pie.phar",
					InstallPath:       "bin/pie.phar",
					ChecksumAlgorithm: checksumAlgorithmSHA256,
				},
			},
		},
	}
	cacheDir := t.TempDir()
	ctx := DownloadContext{
		Client:   server.Client(),
		CacheDir: cacheDir,
		Tool:     PIE,
		Version:  "1.4",
	}
	if err := downloadManifestRequest(ctx, manifest); err != nil {
		t.Fatalf("downloadManifestRequest() error = %v", err)
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

	manifest := loadManifestWithReleaseIndexURL(t, PHPMyAdmin, server.URL)

	plan, err := resolveManifestRequest(server.Client(), manifest, "5.2", "windows", "amd64")
	if err != nil {
		t.Fatalf("resolveManifestRequest(5.2) error = %v", err)
	}
	if plan.ResolvedVersion != "5.2.3" {
		t.Fatalf("resolveManifestRequest(5.2) resolved version = %q, want %q", plan.ResolvedVersion, "5.2.3")
	}
	if plan.Asset.FileName != "phpMyAdmin-5.2.3-all-languages.zip" {
		t.Fatalf("resolveManifestRequest(5.2) file = %q, want phpMyAdmin archive", plan.Asset.FileName)
	}
	if plan.Asset.ArchiveFormat != archiveFormatZip {
		t.Fatalf("resolveManifestRequest(5.2) archive format = %q, want %q", plan.Asset.ArchiveFormat, archiveFormatZip)
	}
	if plan.Asset.ChecksumAlgorithm != checksumAlgorithmSHA256 {
		t.Fatalf("resolveManifestRequest(5.2) checksum algorithm = %q, want %q", plan.Asset.ChecksumAlgorithm, checksumAlgorithmSHA256)
	}
}

// TestResolveManifestRequestGitHubTagModeSkipsReleasesEndpoint verifies that
// github.mode: tag composes the tag directly and authenticates via the commit
// endpoint without calling /releases.
func TestResolveManifestRequestGitHubTagModeSkipsReleasesEndpoint(t *testing.T) {
	commit := "0123456789abcdef0123456789abcdef01234567"
	releasesCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/php/php-sdk-binary-tools/releases":
			releasesCalled = true
			http.Error(w, "releases should not be called", http.StatusInternalServerError)
		case "/repos/php/php-sdk-binary-tools/commits/php-sdk-2.8.3":
			_, _ = w.Write([]byte(`{"sha":"` + commit + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	withTemporaryString(t, &githubAPIBaseURL, server.URL)
	manifest, err := loadBuiltinManifest(PHPSDK)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(php-sdk) error = %v", err)
	}

	plan, err := resolveManifestRequest(server.Client(), manifest, DefaultPHPSDKVersion, "windows", "amd64")
	if err != nil {
		t.Fatalf("resolveManifestRequest(php-sdk) error = %v", err)
	}
	if releasesCalled {
		t.Fatal("GitHub /releases endpoint was called in tag mode")
	}
	if plan.ResolvedVersion != DefaultPHPSDKVersion || plan.Tag != "php-sdk-"+DefaultPHPSDKVersion || plan.Commit != commit {
		t.Fatalf("plan = %#v, want tag mode plan with commit", plan)
	}
}

// TestResolveManifestRequestEnforcesVersionConstraintAfterResolution checks
// that a version label allowed at config time is rejected if it resolves to an
// unsupported patch release.
func TestResolveManifestRequestEnforcesVersionConstraintAfterResolution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"v4.0.2","assets":[{"name":"rabbitmq-server-windows-4.0.2.zip","digest":"sha256:` + strings.Repeat("a", 64) + `"}]}
		]`))
	}))
	defer server.Close()

	withTemporaryString(t, &githubAPIBaseURL, server.URL)
	manifest, err := loadBuiltinManifest(RabbitMQ)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(rabbitmq) error = %v", err)
	}

	_, err = resolveManifestRequest(server.Client(), manifest, "4.0", "windows", "amd64")
	if err == nil || !strings.Contains(err.Error(), "requires >= 4.0.4 and < 5") {
		t.Fatalf("resolveManifestRequest(rabbitmq 4.0 -> 4.0.2) error = %v, want constraint error", err)
	}
}

// TestDownloadManifestAssetRequiresDeclaredChecksumAndUsesChecksumURLFallback
// verifies that declaring a checksum algorithm fails when no digest or
// checksum-url is available, and succeeds when checksum-url supplies the hash.
func TestDownloadManifestAssetRequiresDeclaredChecksumAndUsesChecksumURLFallback(t *testing.T) {
	payload := []byte("verified binary\n")
	sum := checksumForBytes(t, checksumAlgorithmSHA256, payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tool.bin":
			_, _ = w.Write(payload)
		case "/SHASUMS256.txt":
			_, _ = w.Write([]byte(sum + "  tool.bin\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	missingChecksumAsset := downloadAsset{
		FileName:          "tool.bin",
		SourceFileName:    "tool.bin",
		URL:               server.URL + "/tool.bin",
		InstallPath:       "bin/tool",
		ChecksumAlgorithm: checksumAlgorithmSHA256,
	}
	err := downloadManifestAsset(server.Client(), t.TempDir(), "demo", "1.0", "1.0.0", missingChecksumAsset)
	if err == nil || !strings.Contains(err.Error(), "declares sha256 checksum but none was published") {
		t.Fatalf("downloadManifestAsset(no checksum) error = %v, want missing checksum error", err)
	}

	fallbackAsset := missingChecksumAsset
	fallbackAsset.ChecksumURL = server.URL + "/SHASUMS256.txt"
	if err := downloadManifestAsset(server.Client(), t.TempDir(), "demo", "1.0", "1.0.0", fallbackAsset); err != nil {
		t.Fatalf("downloadManifestAsset(checksum-url fallback) error = %v", err)
	}
}

// TestDownloadManifestRequestVerifiesEmbeddedCommitID checks that the generic
// manifest downloader resolves a tag commit SHA, verifies the downloaded zip's
// embedded archive comment, and rejects a mismatched commit.
func TestDownloadManifestRequestVerifiesEmbeddedCommitID(t *testing.T) {
	commit := "0123456789abcdef0123456789abcdef01234567"
	matchingZip := buildZipArchiveWithComment(t, "php-sdk-binary-tools-php-sdk-2.8.3", "phpsdk-starter.bat", []byte("@echo off\r\n"), commit)
	mismatchedZip := buildZipArchiveWithComment(t, "php-sdk-binary-tools-php-sdk-2.8.3", "phpsdk-starter.bat", []byte("@echo off\r\n"), strings.Repeat("f", 40))

	serveMismatch := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/php/php-sdk-binary-tools/commits/php-sdk-2.8.3":
			_, _ = w.Write([]byte(`{"sha":"` + commit + `"}`))
		case "/archive/php-sdk-2.8.3.zip":
			if serveMismatch {
				_, _ = w.Write(mismatchedZip)
				return
			}
			_, _ = w.Write(matchingZip)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	withTemporaryString(t, &githubAPIBaseURL, server.URL)
	manifest, err := loadBuiltinManifest(PHPSDK)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(php-sdk) error = %v", err)
	}
	asset := manifest.Download.Assets["windows-amd64"]
	asset.URL = server.URL + "/archive/{tag}.zip"
	manifest.Download.Assets = map[string]downloadAsset{"all": asset}

	cacheDir := t.TempDir()
	ctx := DownloadContext{
		Client:   server.Client(),
		CacheDir: cacheDir,
		Tool:     PHPSDK,
		Version:  "2.8.3",
	}
	if err := downloadManifestRequest(ctx, manifest); err != nil {
		t.Fatalf("downloadManifestRequest(matching commit) error = %v", err)
	}
	payload, err := CachedToolPayload(cacheDir, PHPSDK, "2.8.3")
	if err != nil {
		t.Fatalf("CachedToolPayload(php-sdk 2.8.3) error = %v", err)
	}
	if payload.DownloadedVersion != "2.8.3" {
		t.Fatalf("CachedToolPayload().DownloadedVersion = %q, want 2.8.3", payload.DownloadedVersion)
	}

	serveMismatch = true
	ctx.CacheDir = t.TempDir()
	err = downloadManifestRequest(ctx, manifest)
	if err == nil || !strings.Contains(err.Error(), "archive commit") {
		t.Fatalf("downloadManifestRequest(mismatched commit) error = %v, want archive commit mismatch error", err)
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

	return buildZipArchiveWithComment(t, rootDir, filePath, contents, "")
}

func buildZipArchiveWithComment(t *testing.T, rootDir, filePath string, contents []byte, comment string) []byte {
	t.Helper()

	buffer := &bytes.Buffer{}
	writer := zip.NewWriter(buffer)
	if comment != "" {
		if err := writer.SetComment(comment); err != nil {
			t.Fatalf("SetComment(zip) error = %v", err)
		}
	}
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

// TestDownloadTextLimitRejectsOversizedBodies verifies the size cap used by
// script HTTP requests detects bodies larger than the limit.
func TestDownloadTextLimitRejectsOversizedBodies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer server.Close()

	if _, err := downloadTextLimit(server.Client(), server.URL, "fixture", 5); err == nil || !strings.Contains(err.Error(), "exceeds 5 bytes") {
		t.Fatalf("downloadTextLimit(5) error = %v, want size error", err)
	}
	text, err := downloadTextLimit(server.Client(), server.URL, "fixture", 10)
	if err != nil || text != "0123456789" {
		t.Fatalf("downloadTextLimit(10) = %q, %v; want full body", text, err)
	}
}
