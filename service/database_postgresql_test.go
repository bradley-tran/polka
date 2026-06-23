package service

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"polka/config"
)

func TestPostgreSQLDatabaseArgumentsAndDefaults(t *testing.T) {
	spec := ManagedDatabaseServerSpec{
		Engine:       toolPostgreSQL,
		DataDir:      filepath.Join("tmp", "data"),
		PasswordFile: filepath.Join("tmp", "password"),
		Port:         5544,
	}
	wantInitialize := []string{
		"--pgdata=" + spec.DataDir,
		"--username=" + ManagedDatabaseUserName,
		"--pwfile=" + spec.PasswordFile,
		"--auth-host=scram-sha-256",
		"--auth-local=trust",
		"--encoding=UTF8",
	}
	if got := DatabaseInitializeArgs(spec); !reflect.DeepEqual(got, wantInitialize) {
		t.Fatalf("DatabaseInitializeArgs() = %#v, want %#v", got, wantInitialize)
	}
	wantStart := []string{"-D", spec.DataDir, "-h", DatabaseListenHost, "-p", "5544"}
	if got := DatabaseStartArgs(spec); !reflect.DeepEqual(got, wantStart) {
		t.Fatalf("DatabaseStartArgs() = %#v, want %#v", got, wantStart)
	}
	if got := EffectiveDatabasePort(&config.DatabaseConfig{Engine: toolPostgreSQL}); got != DefaultPostgreSQLPort {
		t.Fatalf("EffectiveDatabasePort(postgresql) = %d, want %d", got, DefaultPostgreSQLPort)
	}
	if got := DatabaseClientCommand(toolPostgreSQL); got != "psql" {
		t.Fatalf("DatabaseClientCommand(postgresql) = %q, want psql", got)
	}
}

func TestEnsurePostgreSQLCredentialAssets(t *testing.T) {
	root := t.TempDir()
	resolved := ResolvedDatabaseEnvironment{
		Environment: config.Environment{Name: "demo"},
		Database:    &config.DatabaseConfig{Engine: toolPostgreSQL, Version: "17"},
	}
	credentials, err := EnsureManagedDatabaseCredentialAssets(root, resolved)
	if err != nil {
		t.Fatalf("EnsureManagedDatabaseCredentialAssets() error = %v", err)
	}
	if credentials.Port != DefaultPostgreSQLPort || credentials.User != ManagedDatabaseUserName || credentials.DatabaseName != "demo" {
		t.Fatalf("credentials = %#v, want PostgreSQL defaults", credentials)
	}

	password, err := os.ReadFile(DatabasePasswordFilePath(root, "demo"))
	if err != nil {
		t.Fatalf("ReadFile(password) error = %v", err)
	}
	if strings.TrimSpace(string(password)) != credentials.Password {
		t.Fatalf("password file = %q, want generated password", password)
	}
	pgpass, err := os.ReadFile(DatabasePGPassFilePath(root, "demo"))
	if err != nil {
		t.Fatalf("ReadFile(pgpass) error = %v", err)
	}
	wantPGPass := DatabaseListenHost + ":5432:*:" + ManagedDatabaseUserName + ":" + credentials.Password
	if strings.TrimSpace(string(pgpass)) != wantPGPass {
		t.Fatalf("pgpass = %q, want %q", pgpass, wantPGPass)
	}
}

func TestPostgreSQLDataMarkers(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "PG_VERSION"), []byte("17\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(PG_VERSION) error = %v", err)
	}
	initialized, err := DatabaseServerInitialized(ManagedDatabaseServerSpec{Engine: toolPostgreSQL, DataDir: dataDir})
	if err != nil || !initialized {
		t.Fatalf("DatabaseServerInitialized() = %v, %v, want true", initialized, err)
	}
	layout, err := detectDatabaseDataLayout(dataDir)
	if err != nil || layout != toolPostgreSQL {
		t.Fatalf("detectDatabaseDataLayout() = %q, %v, want postgresql", layout, err)
	}
}
