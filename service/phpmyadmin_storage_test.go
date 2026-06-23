package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"polka/config"
	"polka/tools"
)

func TestEnsurePHPMyAdminStorageConfiguredImportsCreateTablesSQL(t *testing.T) {
	projectDir := t.TempDir()
	rootDir := filepath.Join(projectDir, ".polka")
	envsDir := filepath.Join(rootDir, "envs")
	environment := config.Environment{
		Name:       "demo",
		Database:   &config.DatabaseConfig{Engine: toolMySQL, Version: "8.4", Port: 3307},
		PHPMyAdmin: &config.PHPMyAdminConfig{Version: "5.2"},
	}
	ctx := Context{
		ProjectDir:  projectDir,
		RootDir:     rootDir,
		EnvsDir:     envsDir,
		Environment: environment,
		Registry:    tools.NewDefaultRegistry(),
		RuntimeEnv:  func() ([]string, error) { return os.Environ(), nil },
		TLSCert:     func(string) (string, string, error) { return "", "", nil },
	}

	writeFakeDatabaseClient(t, envsDir, toolMySQL, "8.4")
	createTablesPath := filepath.Join(envsDir, toolPHPMyAdmin, "5.2", "sql", "create_tables.sql")
	if err := os.MkdirAll(filepath.Dir(createTablesPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(create_tables.sql dir) error = %v", err)
	}
	createTablesSQL := "CREATE DATABASE IF NOT EXISTS `phpmyadmin`;\nUSE phpmyadmin;\nCREATE TABLE IF NOT EXISTS `pma__relation` (`id` int);\n"
	if err := os.WriteFile(createTablesPath, []byte(createTablesSQL), 0o644); err != nil {
		t.Fatalf("WriteFile(create_tables.sql) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(envsDir, toolPHPMyAdmin, "5.2", "index.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(index.php) error = %v", err)
	}

	state := ManagedDatabaseRuntimeState{
		EnvironmentName: environment.Name,
		Engine:          toolMySQL,
		Version:         "8.4",
		Port:            3307,
		PID:             1234,
	}
	if err := WriteManagedDatabaseState(DatabaseStatePath(rootDir, environment.Name), state); err != nil {
		t.Fatalf("WriteManagedDatabaseState() error = %v", err)
	}

	argsCapturePath := filepath.Join(projectDir, "db-args.txt")
	stdinCapturePath := filepath.Join(projectDir, "db-stdin.sql")
	t.Setenv("POLKA_TEST_DB_ARGS_CAPTURE_PATH", argsCapturePath)
	t.Setenv("POLKA_TEST_DB_STDIN_CAPTURE_PATH", stdinCapturePath)

	err := EnsurePHPMyAdminStorageConfigured(ctx, environment, DatabaseRuntimeHooks{
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
		"--defaults-extra-file=" + DatabaseDefaultsFilePath(rootDir, environment.Name),
		"--protocol=tcp",
		"--host=127.0.0.1",
		"--port=3307",
	} {
		if !strings.Contains(args, expected) {
			t.Fatalf("database client args = %q, want %q", args, expected)
		}
	}
}

func TestEnsurePHPMyAdminStorageConfiguredWarnsAndSkipsPostgreSQL(t *testing.T) {
	environment := config.Environment{
		Name:       "demo",
		Database:   &config.DatabaseConfig{Engine: toolPostgreSQL, Version: "17", Port: 5432},
		PHPMyAdmin: &config.PHPMyAdminConfig{Version: "5.2"},
	}
	warnings := &strings.Builder{}
	ctx := Context{
		Environment: environment,
		Warnf: func(format string, args ...any) {
			warnings.WriteString(fmt.Sprintf(format, args...))
		},
	}

	startedDatabase := false
	err := EnsurePHPMyAdminStorageConfigured(ctx, environment, DatabaseRuntimeHooks{
		StartServer: func(ManagedDatabaseServerSpec) (ManagedDatabaseStartResult, error) {
			startedDatabase = true
			return ManagedDatabaseStartResult{}, nil
		},
	})
	if err != nil {
		t.Fatalf("EnsurePHPMyAdminStorageConfigured() error = %v", err)
	}
	if startedDatabase {
		t.Fatal("EnsurePHPMyAdminStorageConfigured() started PostgreSQL, want storage bootstrap skipped")
	}
	if !strings.Contains(warnings.String(), "phpMyAdmin with PostgreSQL") {
		t.Fatalf("warnings = %q, want PostgreSQL/phpMyAdmin compatibility warning", warnings.String())
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
