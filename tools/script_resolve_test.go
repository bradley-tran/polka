package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// loadScriptResolveManifest loads a demo tool with an unconstrained asset whose
// resolve hook is source, with vars available as ctx.vars.
func loadScriptResolveManifest(t *testing.T, source string, vars map[string]string) pluginManifest {
	t.Helper()
	manifest := `id: demo
install-candidates:
  all:
    - demo
download:
  assets:
    all:
      filename: demo-{version}.zip
      url: https://example.com/demo-{version}.zip
      archive-format: zip
script:
  file: scripts/demo.star
  hooks: [resolve]
`
	loaded := loadScriptFixture(t, manifest, source)
	loaded.Script.Vars = vars

	return loaded
}

// phpTestChecksum returns a well-formed sha256 for release index fixtures.
func phpTestChecksum(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// phpReleaseIndexFixture is a windows.php.net index with both flavors, plus the
// non-variant keys the real index mixes in with them.
func phpReleaseIndexFixture() string {
	return fmt.Sprintf(`{
  "8.4": {
    "version": "8.4.24",
    "source": {"path": "php-8.4.24-src.zip", "sha256": "ignored"},
    "test_pack": {"path": "php-test-pack-8.4.24.zip", "sha256": "ignored"},
    "nts-vs17-x64": {
      "zip": {"path": "php-8.4.24-nts.zip", "sha256": %q},
      "devel_pack": {"path": "php-devel-pack-8.4.24-nts.zip", "sha256": %q}
    },
    "ts-vs17-x64": {
      "zip": {"path": "php-8.4.24-ts.zip", "sha256": %q},
      "devel_pack": {"path": "php-devel-pack-8.4.24-ts.zip", "sha256": %q}
    }
  },
  "8.3": {
    "version": "8.3.30",
    "nts-vs16-x64": {"zip": {"path": "php-8.3.30-nts.zip", "sha256": %q}},
    "ts-vc15-x64": {"zip": {"path": "php-8.3.30-ts.zip", "sha256": %q}}
  },
  "8.2": {
    "version": "8.2.29",
    "ts-vs16-x64": {"zip": {"path": "php-8.2.29-ts.zip", "sha256": %q}}
  }
}`,
		phpTestChecksum("nts"), phpTestChecksum("nts-devel"),
		phpTestChecksum("ts"), phpTestChecksum("ts-devel"),
		phpTestChecksum("83-nts"), phpTestChecksum("83-ts"),
		phpTestChecksum("82-ts"))
}

// newPHPIndexServer serves the fixture index and counts requests to it.
func newPHPIndexServer(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write index response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	return server, &requests
}

// TestPHPResolveSelectsVariant checks flavor and compiler selection for the
// runtime and devel pack, including that flavors pair and never cross.
func TestPHPResolveSelectsVariant(t *testing.T) {
	server, _ := newPHPIndexServer(t, phpReleaseIndexFixture())
	indexURL := server.URL + "/releases/releases.json"

	tests := []struct {
		name         string
		tool         string
		requested    string
		wantVersion  string
		wantFile     string
		wantChecksum string
	}{
		{name: "nts", tool: PHP, requested: "8.4", wantVersion: "8.4.24", wantFile: "php-8.4.24-nts.zip", wantChecksum: phpTestChecksum("nts")},
		{name: "zts", tool: PHPZTS, requested: "8.4", wantVersion: "8.4.24", wantFile: "php-8.4.24-ts.zip", wantChecksum: phpTestChecksum("ts")},
		{name: "patch resolves through series", tool: PHP, requested: "8.4.5", wantVersion: "8.4.24", wantFile: "php-8.4.24-nts.zip", wantChecksum: phpTestChecksum("nts")},
		{name: "older compiler", tool: PHP, requested: "8.3", wantVersion: "8.3.30", wantFile: "php-8.3.30-nts.zip", wantChecksum: phpTestChecksum("83-nts")},
		{name: "oldest compiler", tool: PHPZTS, requested: "8.3", wantVersion: "8.3.30", wantFile: "php-8.3.30-ts.zip", wantChecksum: phpTestChecksum("83-ts")},
		{name: "devel nts suffix", tool: PHPDevel, requested: "8.4-nts", wantVersion: "8.4.24", wantFile: "php-devel-pack-8.4.24-nts.zip", wantChecksum: phpTestChecksum("nts-devel")},
		{name: "devel zts suffix", tool: PHPDevel, requested: "8.4-zts", wantVersion: "8.4.24", wantFile: "php-devel-pack-8.4.24-ts.zip", wantChecksum: phpTestChecksum("ts-devel")},
		{name: "devel without suffix", tool: PHPDevel, requested: "8.4", wantVersion: "8.4.24", wantFile: "php-devel-pack-8.4.24-nts.zip", wantChecksum: phpTestChecksum("nts-devel")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := loadManifestWithScriptVar(t, test.tool, "release-index", indexURL)
			plan, err := resolveManifestRequest(server.Client(), manifest, test.requested, "windows", "amd64")
			if err != nil {
				t.Fatalf("resolveManifestRequest() error = %v", err)
			}
			if plan.ResolvedVersion != test.wantVersion {
				t.Fatalf("ResolvedVersion = %q, want %q", plan.ResolvedVersion, test.wantVersion)
			}
			if plan.Asset.FileName != test.wantFile {
				t.Fatalf("FileName = %q, want %q", plan.Asset.FileName, test.wantFile)
			}
			if want := server.URL + "/releases/" + test.wantFile; plan.Asset.URL != want {
				t.Fatalf("URL = %q, want %q", plan.Asset.URL, want)
			}
			if plan.Asset.Checksum != test.wantChecksum || plan.Asset.ChecksumAlgorithm != checksumAlgorithmSHA256 {
				t.Fatalf("checksum = %s %q, want sha256 %q", plan.Asset.ChecksumAlgorithm, plan.Asset.Checksum, test.wantChecksum)
			}
			if plan.Asset.ArchiveFormat != archiveFormatZip {
				t.Fatalf("ArchiveFormat = %q, want zip", plan.Asset.ArchiveFormat)
			}
		})
	}
}

// TestPHPResolveFailures checks the user-facing failures, in particular that a
// missing flavor never falls back to the other one.
func TestPHPResolveFailures(t *testing.T) {
	server, _ := newPHPIndexServer(t, phpReleaseIndexFixture())

	tests := []struct {
		name      string
		tool      string
		requested string
		want      string
	}{
		{name: "unknown series", tool: PHP, requested: "7.4", want: `php version "7.4" is not available in the Windows release index`},
		{name: "missing nts flavor", tool: PHP, requested: "8.2", want: "no compatible Windows PHP NTS binary found for 8.2.29 on x64"},
		{name: "missing devel pack", tool: PHPDevel, requested: "8.3-nts", want: "Windows PHP 8.3.30 release does not publish a matching devel pack"},
		{name: "devel missing flavor", tool: PHPDevel, requested: "8.2-nts", want: "no compatible Windows PHP NTS binary found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := loadManifestWithScriptVar(t, test.tool, "release-index", server.URL)
			_, err := resolveManifestRequest(server.Client(), manifest, test.requested, "windows", "amd64")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("resolveManifestRequest() error = %v, want containing %q", err, test.want)
			}
			// These are expected failures, not script bugs.
			if strings.Contains(err.Error(), "Traceback") {
				t.Fatalf("resolveManifestRequest() error = %v, want no backtrace", err)
			}
		})
	}
}

// TestPHPResolveRejectsOtherPlatformsWithoutNetwork verifies a non-Windows
// install fails with the documented message before contacting upstream.
func TestPHPResolveRejectsOtherPlatformsWithoutNetwork(t *testing.T) {
	server, requests := newPHPIndexServer(t, phpReleaseIndexFixture())

	for _, test := range []struct {
		tool string
		want string
	}{
		{tool: PHP, want: "automatic php download is only implemented on Windows"},
		{tool: PHPZTS, want: "automatic php download is only implemented on Windows"},
		{tool: PHPDevel, want: "automatic php-devel download is only implemented on Windows"},
	} {
		manifest := loadManifestWithScriptVar(t, test.tool, "release-index", server.URL)
		_, err := resolveManifestRequest(server.Client(), manifest, "8.4", "linux", "amd64")
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%s resolveManifestRequest() error = %v, want containing %q", test.tool, err, test.want)
		}
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("index requests = %d, want none on an unsupported platform", got)
	}
}

// TestPHPManifestsUseSameReleaseIndex keeps the three php scripts reading one
// index, since a runtime and its devel pack must come from the same release.
func TestPHPManifestsUseSameReleaseIndex(t *testing.T) {
	var want string
	for _, tool := range []string{PHP, PHPZTS, PHPDevel} {
		manifest, err := loadBuiltinManifest(tool)
		if err != nil {
			t.Fatalf("loadBuiltinManifest(%s) error = %v", tool, err)
		}
		got := manifest.Script.Vars["release-index"]
		if got == "" {
			t.Fatalf("%s manifest has no release-index", tool)
		}
		if want == "" {
			want = got
		}
		if got != want {
			t.Fatalf("%s release-index = %q, want %q", tool, got, want)
		}
	}
}

// TestPHPDownloadVerifiesAndCaches runs a php plan through the real download
// path: fetch, checksum, cache, and install.
func TestPHPDownloadVerifiesAndCaches(t *testing.T) {
	archive := buildZipArchive(t, "", "php.exe", []byte("php"))
	checksum := sha256.Sum256(archive)
	index := fmt.Sprintf(`{"8.4": {"version": "8.4.24", "nts-vs17-x64": {"zip": {"path": "php-8.4.24-nts.zip", "sha256": %q}}}}`, hex.EncodeToString(checksum[:]))

	mux := http.NewServeMux()
	mux.HandleFunc("/releases/releases.json", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(index)); err != nil {
			t.Errorf("write index: %v", err)
		}
	})
	mux.HandleFunc("/releases/php-8.4.24-nts.zip", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(archive); err != nil {
			t.Errorf("write archive: %v", err)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	manifest := loadManifestWithScriptVar(t, PHP, "release-index", server.URL+"/releases/releases.json")
	plan, err := resolveManifestRequest(server.Client(), manifest, "8.4", "windows", "amd64")
	if err != nil {
		t.Fatalf("resolveManifestRequest() error = %v", err)
	}

	cacheDir := t.TempDir()
	if err := downloadManifestAsset(server.Client(), cacheDir, PHP, "8.4", plan.ResolvedVersion, plan.Asset); err != nil {
		t.Fatalf("downloadManifestAsset() error = %v", err)
	}
	targetDir := t.TempDir()
	payload, err := InstallCachedToolPayload(cacheDir, targetDir, PHP, "8.4")
	if err != nil {
		t.Fatalf("InstallCachedToolPayload() error = %v", err)
	}
	if payload.DownloadedVersion != "8.4.24" {
		t.Fatalf("DownloadedVersion = %q, want 8.4.24", payload.DownloadedVersion)
	}
	data, err := os.ReadFile(filepath.Join(targetDir, "php.exe"))
	if err != nil || string(data) != "php" {
		t.Fatalf("installed php.exe = %q, %v", data, err)
	}

	// A corrupted upstream file must not pass the published checksum.
	archive = append(archive, 0)
	err = downloadManifestAsset(server.Client(), t.TempDir(), PHP, "8.4", plan.ResolvedVersion, plan.Asset)
	if err == nil {
		t.Fatal("downloadManifestAsset(corrupt) error = nil, want checksum failure")
	}
}

// TestScriptResolveRejectsPathsInFileNames verifies upstream metadata cannot
// steer a staging path through a rendered file name.
func TestScriptResolveRejectsPathsInFileNames(t *testing.T) {
	for _, name := range []string{"../evil.zip", `..\evil.zip`, "sub/evil.zip", ".."} {
		manifest := pluginManifest{
			ID: "demo",
			Download: manifestDownload{Assets: map[string]downloadAsset{
				"all": {FileName: "{name}", URL: "https://example.test/x.zip", ArchiveFormat: archiveFormatZip},
			}},
		}
		plan := scriptResolvePlan{Version: "1.0", Values: map[string]string{"name": name}}
		_, err := plan.toRequestPlan("demo", manifest, "1", "windows", "amd64")
		if err == nil || !strings.Contains(err.Error(), "invalid file name") {
			t.Fatalf("toRequestPlan(%q) error = %v, want invalid file name", name, err)
		}
	}
}

// TestHTTPGetJSONReportsUpstreamFailures covers the JSON helper: decoded values
// reach the script, and malformed bodies read as upstream failures, not bugs.
func TestHTTPGetJSONReportsUpstreamFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := `{"version": "1.2.3"}`
		if r.URL.Path == "/bad" {
			body = "<html>not json</html>"
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	manifest := loadScriptResolveManifest(t, "def resolve(ctx):\n    return {\"version\": http.get_json(ctx.vars[\"index\"])[\"version\"]}\n", map[string]string{"index": server.URL + "/good"})
	plan, err := resolveManifestRequest(server.Client(), manifest, "1", "windows", "amd64")
	if err != nil {
		t.Fatalf("resolveManifestRequest(good) error = %v", err)
	}
	if plan.ResolvedVersion != "1.2.3" {
		t.Fatalf("ResolvedVersion = %q, want 1.2.3", plan.ResolvedVersion)
	}

	manifest.Script.Vars = map[string]string{"index": server.URL + "/bad"}
	_, err = resolveManifestRequest(server.Client(), manifest, "1", "windows", "amd64")
	if err == nil || !strings.Contains(err.Error(), "http.get_json") || strings.Contains(err.Error(), "Traceback") {
		t.Fatalf("resolveManifestRequest(bad) error = %v, want an upstream failure with no backtrace", err)
	}
}

// TestHTTPGetEnforcesResponseLimit verifies the documented 8 MiB cap applies
// through the script module, not just the underlying helper.
func TestHTTPGetEnforcesResponseLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(make([]byte, scriptHTTPMaxBytes+1)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	manifest := loadScriptResolveManifest(t, "def resolve(ctx):\n    http.get(ctx.vars[\"index\"])\n    return {\"version\": \"1.0\"}\n", map[string]string{"index": server.URL})
	_, err := resolveManifestRequest(server.Client(), manifest, "1", "windows", "amd64")
	if err == nil || strings.Contains(err.Error(), "Traceback") {
		t.Fatalf("resolveManifestRequest() error = %v, want a size limit failure", err)
	}
}

// TestPostgreSQLResolveVersions checks major labels resolve through the index
// to the newest supported release, and full labels skip the index entirely.
func TestPostgreSQLResolveVersions(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if _, err := w.Write([]byte(`[
  {"major":"17","latestMinor":"4","supported":true},
  {"major":"17","latestMinor":"6","supported":true},
  {"major":"16","latestMinor":"10","supported":true},
  {"major":"15","latestMinor":"14","supported":false}
]`)); err != nil {
			t.Errorf("write index: %v", err)
		}
	}))
	defer server.Close()
	manifest := loadManifestWithScriptVar(t, PostgreSQL, "versions-index", server.URL)

	plan, err := resolveManifestRequest(server.Client(), manifest, "17", "windows", "amd64")
	if err != nil {
		t.Fatalf("resolveManifestRequest(17) error = %v", err)
	}
	if plan.ResolvedVersion != "17.6" {
		t.Fatalf("ResolvedVersion = %q, want 17.6", plan.ResolvedVersion)
	}

	if _, err := resolveManifestRequest(server.Client(), manifest, "15", "windows", "amd64"); err == nil || !strings.Contains(err.Error(), `resolve postgresql version "15": no matching release found`) {
		t.Fatalf("resolveManifestRequest(unsupported) error = %v, want no matching release", err)
	}

	requests.Store(0)
	exact, err := resolveManifestRequest(server.Client(), manifest, "17.4", "windows", "amd64")
	if err != nil || exact.ResolvedVersion != "17.4" {
		t.Fatalf("resolveManifestRequest(17.4) = %#v, %v, want 17.4", exact, err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("index requests for an exact label = %d, want 0", got)
	}
}

// TestPostgreSQLResolveAssets checks each platform renders its upstream asset.
func TestPostgreSQLResolveAssets(t *testing.T) {
	manifest, err := loadBuiltinManifest(PostgreSQL)
	if err != nil {
		t.Fatalf("loadBuiltinManifest() error = %v", err)
	}

	windows, err := resolveManifestRequest(http.DefaultClient, manifest, "17.6", "windows", "amd64")
	if err != nil {
		t.Fatalf("resolveManifestRequest(windows) error = %v", err)
	}
	if windows.Asset.FileName != "postgresql-17.6-1-windows-x64-binaries.zip" || !strings.Contains(windows.Asset.URL, "/postgresql/postgresql-17.6-1-windows-x64-binaries.zip") {
		t.Fatalf("windows asset = %#v", windows.Asset)
	}
	if !windows.Asset.Unwrap.isZero() {
		t.Fatalf("windows asset unwrap = %#v, want none", windows.Asset.Unwrap)
	}

	linux, err := resolveManifestRequest(http.DefaultClient, manifest, "17.6", "linux", "amd64")
	if err != nil {
		t.Fatalf("resolveManifestRequest(linux) error = %v", err)
	}
	if linux.Asset.FileName != "embedded-postgres-binaries-linux-amd64-17.6.0.jar" || !strings.Contains(linux.Asset.URL, "/embedded-postgres-binaries-linux-amd64/17.6.0/") {
		t.Fatalf("linux asset = %#v", linux.Asset)
	}
	if linux.Asset.Unwrap.FileName != "postgresql-17.6-linux-amd64.tar.xz" || linux.Asset.Unwrap.ArchiveFormat != archiveFormatTarXz {
		t.Fatalf("linux asset unwrap = %#v", linux.Asset.Unwrap)
	}
}

// TestPostgreSQLLinuxDownloadUnwraps runs the Linux plan through the real
// download path, proving the nested archive is what gets cached and installed.
func TestPostgreSQLLinuxDownloadUnwraps(t *testing.T) {
	// The root directory is collapsed on install, leaving bin/psql in place.
	inner := buildTarXZArchive(t, "pgsql", "bin/psql", []byte("psql"))
	jar := buildZipArchive(t, "", "postgres-linux-x86_64.txz", inner)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(jar); err != nil {
			t.Errorf("write jar: %v", err)
		}
	}))
	defer server.Close()

	manifest, err := loadBuiltinManifest(PostgreSQL)
	if err != nil {
		t.Fatalf("loadBuiltinManifest() error = %v", err)
	}
	plan, err := resolveManifestRequest(server.Client(), manifest, "17.6", "linux", "amd64")
	if err != nil {
		t.Fatalf("resolveManifestRequest() error = %v", err)
	}
	plan.Asset.URL = server.URL + "/postgres.jar"

	cacheDir := t.TempDir()
	if err := downloadManifestAsset(server.Client(), cacheDir, PostgreSQL, "17.6", plan.ResolvedVersion, plan.Asset); err != nil {
		t.Fatalf("downloadManifestAsset() error = %v", err)
	}
	targetDir := t.TempDir()
	if _, err := InstallCachedToolPayload(cacheDir, targetDir, PostgreSQL, "17.6"); err != nil {
		t.Fatalf("InstallCachedToolPayload() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(targetDir, "bin", "psql"))
	if err != nil || string(data) != "psql" {
		t.Fatalf("installed psql = %q, %v", data, err)
	}
}

// unwrapTestAsset describes a zip download with one nested tar.xz file.
func unwrapTestAsset(url string) downloadAsset {
	return downloadAsset{
		FileName:      "outer.zip",
		URL:           url,
		ArchiveFormat: archiveFormatZip,
		Unwrap:        downloadUnwrap{Path: "nested/inner.txz", FileName: "inner.tar.xz", ArchiveFormat: archiveFormatTarXz},
	}
}

// TestUnwrapChecksOuterChecksum verifies the published checksum applies to the
// outer download, and that the cache then validates the nested file on its own.
func TestUnwrapChecksOuterChecksum(t *testing.T) {
	inner := buildTarXZArchive(t, "", "tool", []byte("tool"))
	outer := buildZipArchive(t, "", "nested/inner.txz", inner)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(outer); err != nil {
			t.Errorf("write archive: %v", err)
		}
	}))
	defer server.Close()

	sum := sha256.Sum256(outer)
	asset := unwrapTestAsset(server.URL)
	asset.ChecksumAlgorithm = checksumAlgorithmSHA256
	asset.Checksum = hex.EncodeToString(sum[:])
	cacheDir := t.TempDir()
	if err := downloadManifestAsset(server.Client(), cacheDir, "demo", "1", "1.0", asset); err != nil {
		t.Fatalf("downloadManifestAsset() error = %v", err)
	}
	payload, err := CachedToolPayload(cacheDir, "demo", "1")
	if err != nil {
		t.Fatalf("CachedToolPayload() error = %v, want the nested file to validate", err)
	}
	if filepath.Base(payload.PayloadPath) != "inner.tar.xz" {
		t.Fatalf("PayloadPath = %q, want inner.tar.xz", payload.PayloadPath)
	}

	wrong := sha256.Sum256([]byte("something else"))
	asset.Checksum = hex.EncodeToString(wrong[:])
	if err := downloadManifestAsset(server.Client(), t.TempDir(), "demo", "1", "1.0", asset); err == nil {
		t.Fatal("downloadManifestAsset(wrong checksum) error = nil, want failure on the outer file")
	}
}

// TestUnwrapMissingNestedFile verifies an outer archive without the declared
// file fails clearly instead of caching the wrong payload.
func TestUnwrapMissingNestedFile(t *testing.T) {
	outer := buildZipArchive(t, "", "other.txt", []byte("other"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(outer); err != nil {
			t.Errorf("write archive: %v", err)
		}
	}))
	defer server.Close()

	err := downloadManifestAsset(server.Client(), t.TempDir(), "demo", "1", "1.0", unwrapTestAsset(server.URL))
	if err == nil || !strings.Contains(err.Error(), "nested archive") {
		t.Fatalf("downloadManifestAsset() error = %v, want nested archive failure", err)
	}
}

// TestUnwrapManifestValidation covers the static checks on an unwrap block.
func TestUnwrapManifestValidation(t *testing.T) {
	base := "id: demo\ninstall-candidates:\n  all:\n    - demo\ndownload:\n  assets:\n    all:\n      filename: outer.zip\n      url: https://example.test/outer.zip\n"
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "parent escape", body: "      archive-format: zip\n      unwrap:\n        path: ../inner.txz\n        filename: inner.tar.xz\n        archive-format: tar.xz\n", want: "must stay inside"},
		{name: "rooted", body: "      archive-format: zip\n      unwrap:\n        path: /inner.txz\n        filename: inner.tar.xz\n        archive-format: tar.xz\n", want: "unwrap"},
		{name: "no path", body: "      archive-format: zip\n      unwrap:\n        filename: inner.tar.xz\n        archive-format: tar.xz\n", want: "cannot be empty"},
		{name: "no filename", body: "      archive-format: zip\n      unwrap:\n        path: inner.txz\n        archive-format: tar.xz\n", want: "requires filename"},
		{name: "bad format", body: "      archive-format: zip\n      unwrap:\n        path: inner.txz\n        filename: inner.tar.xz\n        archive-format: rar\n", want: "unsupported archive format"},
		{name: "install path", body: "      install-path: demo\n      unwrap:\n        path: inner.txz\n        filename: inner.tar.xz\n        archive-format: tar.xz\n", want: "install-path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parsePluginManifest([]byte(base + test.body)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parsePluginManifest() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
