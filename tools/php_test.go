package tools

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectPHPWindowsAssetUsesStrictFlavor(t *testing.T) {
	release := phpWindowsRelease{
		Version: "8.4.8",
		Variants: map[string]phpWindowsVariant{
			"nts-vs17-x64": {Zip: phpWindowsAsset{Path: "php-nts.zip"}},
			"ts-vs17-x64":  {Zip: phpWindowsAsset{Path: "php-ts.zip"}},
		},
	}

	nts, err := selectPHPWindowsAsset(release, false)
	if err != nil || nts.Path != "php-nts.zip" {
		t.Fatalf("selectPHPWindowsAsset(NTS) = %#v, %v", nts, err)
	}
	zts, err := selectPHPWindowsAsset(release, true)
	if err != nil || zts.Path != "php-ts.zip" {
		t.Fatalf("selectPHPWindowsAsset(ZTS) = %#v, %v", zts, err)
	}
}

func TestSelectPHPWindowsAssetDoesNotCrossFlavor(t *testing.T) {
	release := phpWindowsRelease{
		Version: "8.4.8",
		Variants: map[string]phpWindowsVariant{
			"ts-vs17-x64": {Zip: phpWindowsAsset{Path: "php-ts.zip"}},
		},
	}

	_, err := selectPHPWindowsAsset(release, false)
	if err == nil || !strings.Contains(err.Error(), "NTS") {
		t.Fatalf("selectPHPWindowsAsset(NTS) error = %v, want strict NTS failure", err)
	}
}

func TestEnsureInstalledPHPOpenSSLConfigWritesFallback(t *testing.T) {
	envsDir := t.TempDir()

	path, err := ensureInstalledPHPOpenSSLConfig(envsDir, PHP, "8.4")
	if err != nil {
		t.Fatalf("ensureInstalledPHPOpenSSLConfig() error = %v", err)
	}
	if path != filepath.Join(envsDir, PHP, "8.4", filepath.FromSlash(phpOpenSSLConfigPath)) {
		t.Fatalf("openssl config path = %q, want project-local PHP config path", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(openssl.cnf) error = %v", err)
	}
	config := string(data)
	for _, want := range []string{"HOME = .", "[req]", "distinguished_name = req_distinguished_name", "[req_distinguished_name]"} {
		if !strings.Contains(config, want) {
			t.Fatalf("openssl.cnf = %q, want %q", config, want)
		}
	}
}

func TestEnsureInstalledPHPOpenSSLConfigPreservesExisting(t *testing.T) {
	envsDir := t.TempDir()
	path := installedPHPOpenSSLConfigPath(envsDir, PHP, "8.4")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(openssl config dir) error = %v", err)
	}
	if err := os.WriteFile(path, []byte("custom-openssl-config\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(openssl.cnf) error = %v", err)
	}

	got, err := ensureInstalledPHPOpenSSLConfig(envsDir, PHP, "8.4")
	if err != nil {
		t.Fatalf("ensureInstalledPHPOpenSSLConfig() error = %v", err)
	}
	if got != path {
		t.Fatalf("openssl config path = %q, want %q", got, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(openssl.cnf) error = %v", err)
	}
	if string(data) != "custom-openssl-config\n" {
		t.Fatalf("openssl.cnf = %q, want existing contents preserved", data)
	}
}

// TestDownloadLinuxPHPCachesHerdLiteBinary verifies that the Linux downloader
// requests the herd-lite path for the requested series and architecture, and
// caches the binary as an executable file payload installed at bin/php.
func TestDownloadLinuxPHPCachesHerdLiteBinary(t *testing.T) {
	tests := []struct {
		name     string
		goarch   string
		wantPath string
	}{
		{name: "amd64", goarch: "amd64", wantPath: "/herd-lite/linux/x64/8.4/php"},
		{name: "arm64", goarch: "arm64", wantPath: "/herd-lite/linux/arm64/8.4/php"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := append(append([]byte{}, elfMagic...), []byte(" herd-lite php\n")...)
			requested := ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.wantPath {
					http.NotFound(w, r)
					return
				}
				requested = r.URL.Path
				_, _ = w.Write(payload)
			}))
			defer server.Close()

			withTemporaryString(t, &phpHerdLiteBaseURL, server.URL)

			cacheDir := t.TempDir()
			if err := downloadLinuxPHP(server.Client(), cacheDir, PHP, "8.4.3", false, test.goarch); err != nil {
				t.Fatalf("downloadLinuxPHP() error = %v", err)
			}
			if requested != test.wantPath {
				t.Fatalf("requested path = %q, want %q", requested, test.wantPath)
			}

			cachedPayload, err := CachedToolPayload(cacheDir, PHP, "8.4.3")
			if err != nil {
				t.Fatalf("CachedToolPayload(php 8.4.3) error = %v", err)
			}
			data, err := os.ReadFile(cachedPayload.PayloadPath)
			if err != nil {
				t.Fatalf("ReadFile(cached php) error = %v", err)
			}
			if string(data) != string(payload) {
				t.Fatalf("cached php = %q, want %q", string(data), string(payload))
			}

			metadata, err := readToolCacheMetadata(filepath.Join(cacheDir, PHP), PHP)
			if err != nil {
				t.Fatalf("readToolCacheMetadata(php) error = %v", err)
			}
			entry := metadata.Versions["8.4.3"]
			if entry.PayloadKind != payloadKindFile || entry.InstallPath != "bin/php" {
				t.Fatalf("metadata entry = %#v, want file payload installed at bin/php", entry)
			}
			// herd-lite publishes no checksum sidecar, so the cache must record
			// the SHA-256 it computed over the download to stay verifiable.
			wantChecksum := checksumForBytes(t, checksumAlgorithmSHA256, payload)
			if entry.ChecksumAlgorithm != checksumAlgorithmSHA256 || entry.Checksum != wantChecksum {
				t.Fatalf("metadata checksum = %q/%q, want sha256/%s", entry.ChecksumAlgorithm, entry.Checksum, wantChecksum)
			}

			installDir := t.TempDir()
			if _, err := InstallCachedToolPayload(cacheDir, installDir, PHP, "8.4.3"); err != nil {
				t.Fatalf("InstallCachedToolPayload(php 8.4.3) error = %v", err)
			}
			info, err := os.Stat(filepath.Join(installDir, "bin", "php"))
			if err != nil {
				t.Fatalf("Stat(installed php) error = %v", err)
			}
			if info.Mode()&0o111 == 0 {
				t.Fatalf("Mode(installed php) = %v, want the executable bit set", info.Mode())
			}
		})
	}
}

// TestDownloadLinuxPHPRejectsNonBinaryPayload ensures an error page served with
// a 200 status is not cached as a PHP runtime, the risk of an unsigned download.
func TestDownloadLinuxPHPRejectsNonBinaryPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>AccessDenied</body></html>"))
	}))
	defer server.Close()

	withTemporaryString(t, &phpHerdLiteBaseURL, server.URL)

	cacheDir := t.TempDir()
	err := downloadLinuxPHP(server.Client(), cacheDir, PHP, "8.4", false, "amd64")
	if err == nil || !strings.Contains(err.Error(), "not a Linux executable") {
		t.Fatalf("downloadLinuxPHP() error = %v, want non-executable payload rejection", err)
	}
	if _, err := CachedToolPayload(cacheDir, PHP, "8.4"); !IsCacheMiss(err) {
		t.Fatalf("CachedToolPayload(php 8.4) error = %v, want cache miss", err)
	}
}

// TestDownloadLinuxPHPReportsMissingSeries keeps an unpublished series or a
// herd-lite outage from surfacing as an opaque HTTP failure.
func TestDownloadLinuxPHPReportsMissingSeries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	withTemporaryString(t, &phpHerdLiteBaseURL, server.URL)

	err := downloadLinuxPHP(server.Client(), t.TempDir(), PHP, "7.4", false, "amd64")
	if err == nil || !strings.Contains(err.Error(), "php 7.4 is not available from herd-lite for linux/x64") {
		t.Fatalf("downloadLinuxPHP() error = %v, want unavailable series failure", err)
	}
}

// TestDownloadLinuxPHPRejectsThreadSafeRuntime documents that herd-lite ships
// only non-thread-safe builds, so php-zts cannot be downloaded on Linux.
func TestDownloadLinuxPHPRejectsThreadSafeRuntime(t *testing.T) {
	err := downloadLinuxPHP(nil, t.TempDir(), PHPZTS, "8.4", true, "amd64")
	if err == nil || !strings.Contains(err.Error(), "non-thread-safe") {
		t.Fatalf("downloadLinuxPHP(ZTS) error = %v, want thread-safe rejection", err)
	}
}

// TestPHPHerdLiteArchitecture covers the herd-lite architecture path segments.
func TestPHPHerdLiteArchitecture(t *testing.T) {
	if architecture, err := phpHerdLiteArchitecture("amd64"); err != nil || architecture != "x64" {
		t.Fatalf("phpHerdLiteArchitecture(amd64) = %q, %v, want x64", architecture, err)
	}
	if architecture, err := phpHerdLiteArchitecture("arm64"); err != nil || architecture != "arm64" {
		t.Fatalf("phpHerdLiteArchitecture(arm64) = %q, %v, want arm64", architecture, err)
	}
	if _, err := phpHerdLiteArchitecture("386"); err == nil || !strings.Contains(err.Error(), "linux/386") {
		t.Fatalf("phpHerdLiteArchitecture(386) error = %v, want unsupported architecture failure", err)
	}
}
