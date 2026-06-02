package backend

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnsurePHPMyAdminStorageConfiguredImportsCreateTablesSQL(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	environment := Environment{
		Name:       "demo",
		Database:   &DatabaseConfig{Engine: toolMySQL, Version: "8.4", Port: 3307},
		PHPMyAdmin: &PHPMyAdminConfig{Version: "5.2"},
	}

	writeFakeDatabaseClient(t, store.EnvsDir, toolMySQL, "8.4")
	createTablesPath := filepath.Join(store.EnvsDir, toolPHPMyAdmin, "5.2", "sql", "create_tables.sql")
	if err := os.MkdirAll(filepath.Dir(createTablesPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(create_tables.sql dir) error = %v", err)
	}
	createTablesSQL := "CREATE DATABASE IF NOT EXISTS `phpmyadmin`;\nUSE phpmyadmin;\nCREATE TABLE IF NOT EXISTS `pma__relation` (`id` int);\n"
	if err := os.WriteFile(createTablesPath, []byte(createTablesSQL), 0o644); err != nil {
		t.Fatalf("WriteFile(create_tables.sql) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.EnvsDir, toolPHPMyAdmin, "5.2", "index.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(index.php) error = %v", err)
	}

	state := ManagedDatabaseRuntimeState{
		EnvironmentName: environment.Name,
		Engine:          toolMySQL,
		Version:         "8.4",
		Port:            3307,
		PID:             1234,
	}
	if err := WriteManagedDatabaseState(DatabaseStatePath(store.RootDir, environment.Name), state); err != nil {
		t.Fatalf("WriteManagedDatabaseState() error = %v", err)
	}

	argsCapturePath := filepath.Join(projectDir, "db-args.txt")
	stdinCapturePath := filepath.Join(projectDir, "db-stdin.sql")
	t.Setenv("POLKA_TEST_DB_ARGS_CAPTURE_PATH", argsCapturePath)
	t.Setenv("POLKA_TEST_DB_STDIN_CAPTURE_PATH", stdinCapturePath)

	err := EnsurePHPMyAdminStorageConfigured(store, environment, DatabaseRuntimeHooks{
		PingAddress: func(address string) bool {
			return address == DatabaseAddress(3307)
		},
	})
	if err != nil {
		t.Fatalf("EnsurePHPMyAdminStorageConfigured() error = %v", err)
	}

	importedSQL, err := os.ReadFile(stdinCapturePath)
	if err != nil {
		t.Fatalf("ReadFile(stdin capture) error = %v", err)
	}
	if string(importedSQL) != createTablesSQL {
		t.Fatalf("imported SQL = %q, want create_tables.sql contents", importedSQL)
	}

	argsData, err := os.ReadFile(argsCapturePath)
	if err != nil {
		t.Fatalf("ReadFile(args capture) error = %v", err)
	}
	args := string(argsData)
	for _, expected := range []string{
		"--defaults-extra-file=" + DatabaseDefaultsFilePath(store.RootDir, environment.Name),
		"--protocol=tcp",
		"--host=127.0.0.1",
		"--port=3307",
	} {
		if !strings.Contains(args, expected) {
			t.Fatalf("database client args = %q, want %q", args, expected)
		}
	}
}

func writeFakeDatabaseClient(t *testing.T, envsDir, engine, version string) string {
	t.Helper()

	clientPath := filepath.Join(envsDir, engine, version, "bin", engine)
	if runtime.GOOS == "windows" {
		clientPath += ".cmd"
	}
	if err := os.MkdirAll(filepath.Dir(clientPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(fake database client dir) error = %v", err)
	}
	if err := os.WriteFile(clientPath, fakeDatabaseClientScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(fake database client) error = %v", err)
	}

	return clientPath
}

func fakeDatabaseClientScript() []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho %* > \"%POLKA_TEST_DB_ARGS_CAPTURE_PATH%\"\r\npowershell -NoProfile -Command \"[IO.File]::WriteAllText($env:POLKA_TEST_DB_STDIN_CAPTURE_PATH, [Console]::In.ReadToEnd())\"\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf '%s\\n' \"$*\" > \"$POLKA_TEST_DB_ARGS_CAPTURE_PATH\"\ncat > \"$POLKA_TEST_DB_STDIN_CAPTURE_PATH\"\n")
}
