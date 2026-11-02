package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestResolveMariaDBBootstrapTarget covers binary archive layouts and the legacy
// helper name without requiring a MariaDB installation on the test host.
func TestResolveMariaDBBootstrapTarget(t *testing.T) {
	directories := []string{"bin", "scripts", "."}
	names := []string{"mariadb-install-db", "mysql_install_db"}
	if runtime.GOOS == "windows" {
		directories = []string{"bin", "."}
		names = []string{"mariadb-install-db.exe", "mysql_install_db.exe"}
	}

	for _, directory := range directories {
		for _, name := range names {
			relativePath := filepath.Join(directory, name)
			t.Run(relativePath, func(t *testing.T) {
				installDir := t.TempDir()
				want := filepath.Join(installDir, relativePath)
				if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
					t.Fatalf("MkdirAll(helper directory) error = %v", err)
				}
				if err := os.WriteFile(want, []byte("helper\n"), 0o755); err != nil {
					t.Fatalf("WriteFile(helper) error = %v", err)
				}

				got, err := resolveDatabaseBootstrapTarget(installDir, toolMariaDB)
				if err != nil {
					t.Fatalf("resolveDatabaseBootstrapTarget() error = %v", err)
				}
				if got != want {
					t.Fatalf("resolveDatabaseBootstrapTarget() = %q, want %q", got, want)
				}
			})
		}
	}
}

// TestResolveMariaDBBootstrapTargetMissing ensures an incomplete installation
// still reports which installation is missing its bootstrap helper.
func TestResolveMariaDBBootstrapTargetMissing(t *testing.T) {
	installDir := t.TempDir()
	if _, err := resolveDatabaseBootstrapTarget(installDir, toolMariaDB); err == nil {
		t.Fatal("resolveDatabaseBootstrapTarget() error = nil, want missing helper error")
	} else if !strings.Contains(err.Error(), "mariadb bootstrap helper was not found under "+installDir) {
		t.Fatalf("resolveDatabaseBootstrapTarget() error = %q, want missing helper and installation path", err)
	}
}
