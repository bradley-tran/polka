package tools

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"polka/config"
)

func TestExtractEmbeddedPostgreSQLPayload(t *testing.T) {
	inner := buildTarXZArchive(t, "", "bin/psql", []byte("psql"))
	jar := buildZipArchive(t, "", "postgres-linux-x86_64.txz", inner)
	jarPath := filepath.Join(t.TempDir(), "postgres.jar")
	if err := os.WriteFile(jarPath, jar, 0o644); err != nil {
		t.Fatalf("WriteFile(jar) error = %v", err)
	}
	payloadPath, err := extractEmbeddedPostgreSQLPayload(jarPath, t.TempDir())
	if err != nil {
		t.Fatalf("extractEmbeddedPostgreSQLPayload() error = %v", err)
	}
	installDir := t.TempDir()
	if err := extractTarXZArchive(payloadPath, installDir); err != nil {
		t.Fatalf("extractTarXZArchive(payload) error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(installDir, "bin", "psql"))
	if err != nil || string(data) != "psql" {
		t.Fatalf("installed psql = %q, %v", data, err)
	}
}

func TestResolvePostgreSQLReleaseVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
  {"major":"17","latestMinor":"4","supported":true},
  {"major":"17","latestMinor":"6","supported":true},
  {"major":"16","latestMinor":"10","supported":true},
  {"major":"15","latestMinor":"14","supported":false}
]`))
	}))
	defer server.Close()
	withTemporaryString(t, &postgreSQLVersionsURL, server.URL)

	resolved, err := resolvePostgreSQLReleaseVersion(server.Client(), "17")
	if err != nil {
		t.Fatalf("resolvePostgreSQLReleaseVersion() error = %v", err)
	}
	if resolved != "17.6" {
		t.Fatalf("resolvePostgreSQLReleaseVersion() = %q, want 17.6", resolved)
	}

	exact, err := resolvePostgreSQLReleaseVersion(server.Client(), "17.4")
	if err != nil || exact != "17.4" {
		t.Fatalf("resolvePostgreSQLReleaseVersion(exact) = %q, %v, want 17.4", exact, err)
	}
}

func TestResolvePostgreSQLDownloadAssets(t *testing.T) {
	for _, test := range []struct {
		goos     string
		fileName string
		format   archiveFormat
	}{
		{goos: "windows", fileName: "postgresql-17.6-1-windows-x64-binaries.zip", format: archiveFormatZip},
		{goos: "linux", fileName: "embedded-postgres-binaries-linux-amd64-17.6.0.jar", format: archiveFormatZip},
	} {
		resolved, asset, err := resolveDatabaseDownloadAsset(http.DefaultClient, PostgreSQL, "17.6", test.goos, "amd64")
		if err != nil {
			t.Fatalf("resolveDatabaseDownloadAsset(%s) error = %v", test.goos, err)
		}
		if resolved != "17.6" || asset.FileName != test.fileName || asset.ArchiveFormat != test.format {
			t.Fatalf("resolveDatabaseDownloadAsset(%s) = %q, %#v", test.goos, resolved, asset)
		}
		if test.goos == "windows" && !strings.Contains(asset.URL, "/postgresql/"+test.fileName) {
			t.Fatalf("asset URL = %q, want EnterpriseDB PostgreSQL archive", asset.URL)
		}
		if test.goos == "linux" && !strings.Contains(asset.URL, "/embedded-postgres-binaries-linux-amd64/") {
			t.Fatalf("asset URL = %q, want embedded PostgreSQL Maven archive", asset.URL)
		}
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

	active := registry.ActiveCommandNames(&config.Environment{PostgreSQLVersion: "17"})
	if !containsString(active, PSQL) {
		t.Fatalf("ActiveCommandNames() = %#v, want psql", active)
	}
}

func TestPHPMyAdminRejectsPostgreSQL(t *testing.T) {
	environment := config.Environment{
		PostgreSQLVersion: "17",
		Database:          &config.DatabaseConfig{Engine: PostgreSQL, Version: "17"},
		PHPMyAdmin:        &config.PHPMyAdminConfig{Version: "5.2"},
	}
	err := NewDefaultRegistry().ValidateEnvironment(environment)
	if err == nil || !strings.Contains(err.Error(), "phpmyadmin does not support") {
		t.Fatalf("ValidateEnvironment() error = %v, want phpmyadmin incompatibility", err)
	}
}
