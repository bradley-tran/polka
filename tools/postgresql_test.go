package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"polka/config"
)

// TestPostgreSQLResolveVersions checks major, minor, and exact build labels
// select published GitHub releases while skipping drafts and prereleases.
func TestPostgreSQLResolveVersions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/theseus-rs/postgresql-binaries/releases" {
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
			return
		}
		if _, err := w.Write([]byte(`[
  {"tag_name":"18.0.0"},
  {"tag_name":"17.6.0"},
  {"tag_name":"17.7.0"},
  {"tag_name":"17.6.2"},
  {"tag_name":"17.8.0","prerelease":true},
  {"tag_name":"17.9.0","draft":true},
  {"tag_name":"16.10.0"}
]`)); err != nil {
			t.Errorf("write releases: %v", err)
		}
	}))
	defer server.Close()
	withTemporaryString(t, &githubAPIBaseURL, server.URL)
	manifest, err := loadBuiltinManifest(PostgreSQL)
	if err != nil {
		t.Fatalf("loadBuiltinManifest() error = %v", err)
	}

	for _, test := range []struct{ requested, want string }{
		{"17", "17.7.0"},
		{"17.6", "17.6.2"},
		{"17.6.0", "17.6.0"},
		{"16", "16.10.0"},
	} {
		t.Run(test.requested, func(t *testing.T) {
			plan, err := resolveManifestRequest(server.Client(), manifest, test.requested, "windows", "amd64")
			if err != nil {
				t.Fatalf("resolveManifestRequest() error = %v", err)
			}
			if plan.ResolvedVersion != test.want || plan.Tag != test.want {
				t.Fatalf("version/tag = %q/%q, want %q", plan.ResolvedVersion, plan.Tag, test.want)
			}
		})
	}
	if _, err := resolveManifestRequest(server.Client(), manifest, "15", "windows", "amd64"); err == nil || !strings.Contains(err.Error(), `resolve postgresql version "15": no matching release found`) {
		t.Fatalf("resolveManifestRequest(unavailable) error = %v, want no matching release", err)
	}
}

// TestPostgreSQLDownloadArchives verifies both platforms use direct upstream
// archives and install bin/psql, with GitHub digests or checksum-file fallback.
func TestPostgreSQLDownloadArchives(t *testing.T) {
	for _, test := range []struct {
		goos, target, executable string
		format                   archiveFormat
		build                    func(*testing.T, string, string, []byte) []byte
		useDigest                bool
	}{
		{"windows", "x86_64-pc-windows-msvc", "psql.exe", archiveFormatZip, buildZipArchive, true},
		{"linux", "x86_64-unknown-linux-gnu", "psql", archiveFormatTarGz, buildTarGzipArchive, false},
	} {
		t.Run(test.goos, func(t *testing.T) {
			root := "postgresql-17.6.0-" + test.target
			filename := root + "." + string(test.format)
			archive := test.build(t, root, "bin/"+test.executable, []byte("psql"))
			checksum := checksumForBytes(t, checksumAlgorithmSHA256, archive)
			digest := ""
			if test.useDigest {
				digest = "sha256:" + checksum
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/theseus-rs/postgresql-binaries/releases":
					releases := []githubRelease{{TagName: "17.6.0", Assets: []githubReleaseAsset{{Name: filename, Digest: digest}}}}
					if err := json.NewEncoder(w).Encode(releases); err != nil {
						t.Errorf("write releases: %v", err)
					}
				case "/" + filename:
					if _, err := w.Write(archive); err != nil {
						t.Errorf("write archive: %v", err)
					}
				case "/" + filename + ".sha256":
					if _, err := fmt.Fprintf(w, "%s  %s\n", checksum, filename); err != nil {
						t.Errorf("write checksum: %v", err)
					}
				default:
					t.Errorf("unexpected request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			withTemporaryString(t, &githubAPIBaseURL, server.URL)
			manifest, err := loadBuiltinManifest(PostgreSQL)
			if err != nil {
				t.Fatalf("loadBuiltinManifest() error = %v", err)
			}
			plan, err := resolveManifestRequest(server.Client(), manifest, "17.6", test.goos, "amd64")
			if err != nil {
				t.Fatalf("resolveManifestRequest() error = %v", err)
			}
			wantURL := "https://github.com/theseus-rs/postgresql-binaries/releases/download/17.6.0/" + filename
			asset := plan.Asset
			if asset.FileName != filename || asset.SourceFileName != filename || asset.URL != wantURL || asset.ChecksumURL != wantURL+".sha256" || asset.ArchiveFormat != test.format || !asset.Unwrap.isZero() {
				t.Fatalf("asset = %#v, want direct archive %s", asset, wantURL)
			}
			wantChecksum := ""
			if test.useDigest {
				wantChecksum = checksum
			}
			if asset.ChecksumAlgorithm != checksumAlgorithmSHA256 || asset.Checksum != wantChecksum {
				t.Fatalf("checksum = %q %q, want sha256 %q", asset.ChecksumAlgorithm, asset.Checksum, wantChecksum)
			}

			asset.URL = server.URL + "/" + filename
			asset.ChecksumURL = asset.URL + ".sha256"
			cacheDir := t.TempDir()
			if err := downloadManifestAsset(server.Client(), cacheDir, PostgreSQL, "17.6", plan.ResolvedVersion, asset); err != nil {
				t.Fatalf("downloadManifestAsset() error = %v", err)
			}
			targetDir := t.TempDir()
			if _, err := InstallCachedToolPayload(cacheDir, targetDir, PostgreSQL, "17.6"); err != nil {
				t.Fatalf("InstallCachedToolPayload() error = %v", err)
			}
			data, err := os.ReadFile(filepath.Join(targetDir, "bin", test.executable))
			if err != nil || string(data) != "psql" {
				t.Fatalf("installed psql = %q, %v", data, err)
			}
		})
	}
}

func TestPostgreSQLPluginUsesPSQLDispatch(t *testing.T) {
	registry := NewDefaultRegistry()
	request, err := registry.ResolveDispatchRequest(PSQL)
	if err != nil {
		t.Fatalf("ResolveDispatchRequest(psql) error = %v", err)
	}
	if request.ConfigTool != PostgreSQL || request.Executable != PSQL {
		t.Fatalf("ResolveDispatchRequest(psql) = %#v, want postgresql/psql", request)
	}

	active := registry.ActiveCommandNames(&config.Environment{Tools: config.ToolsConfig{PostgreSQL: "17"}})
	if !containsString(active, PSQL) {
		t.Fatalf("ActiveCommandNames() = %#v, want psql", active)
	}
}

// TestPostgreSQLPSQLDispatchResolvesInstallCandidates guards against the psql
// dispatch command (which differs from the postgresql tool id) resolving to no
// candidates. It has no explicit dispatch-candidates entry and must fall back to
// the manifest's install-candidates (the psql binary).
func TestPostgreSQLPSQLDispatchResolvesInstallCandidates(t *testing.T) {
	registry := NewDefaultRegistry()
	candidates := registry.DispatchCandidates("root", PostgreSQL, PSQL, "17")
	if len(candidates) == 0 {
		t.Fatalf("DispatchCandidates(postgresql, psql) = %#v, want non-empty", candidates)
	}

	wantSuffix := filepath.Join("root", PostgreSQL, "17", "bin", "psql")
	found := false
	for _, candidate := range candidates {
		if strings.Contains(candidate, wantSuffix) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("DispatchCandidates(postgresql, psql) = %#v, want a candidate containing %q", candidates, wantSuffix)
	}
}

func TestPHPMyAdminAllowsPostgreSQL(t *testing.T) {
	environment := config.Environment{
		Tools:    config.ToolsConfig{PostgreSQL: "17", PHPMyAdmin: "5.2"},
		Database: &config.DatabaseConfig{Engine: PostgreSQL, Version: "17"},
	}
	if err := NewDefaultRegistry().ValidateEnvironment(environment); err != nil {
		t.Fatalf("ValidateEnvironment() error = %v, want PostgreSQL/phpMyAdmin warning-only pairing", err)
	}
}
