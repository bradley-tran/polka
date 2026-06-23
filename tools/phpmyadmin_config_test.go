package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"polka/config"
)

func TestConfigureInstalledPHPMyAdminWritesGeneratedConfig(t *testing.T) {
	installDir := t.TempDir()
	rootDir := t.TempDir()
	indexPath := filepath.Join(installDir, "index.php")
	if err := os.WriteFile(indexPath, []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(index.php) error = %v", err)
	}

	err := configureInstalledPHPMyAdmin(InstallContext{
		RootDir: rootDir,
		Environment: config.Environment{
			Name:     "demo",
			Database: &config.DatabaseConfig{Engine: MariaDB, Version: "11.8", Port: 3307},
		},
		Result: InstallResult{
			Tool:       PHPMyAdmin,
			Version:    "5.2",
			TargetPath: indexPath,
		},
	})
	if err != nil {
		t.Fatalf("configureInstalledPHPMyAdmin() error = %v", err)
	}

	configData, err := os.ReadFile(filepath.Join(installDir, phpMyAdminConfigFileName))
	if err != nil {
		t.Fatalf("ReadFile(config.inc.php) error = %v", err)
	}
	configText := string(configData)
	secretPattern := regexp.MustCompile(`\$cfg\['blowfish_secret'\] = '([^']{32})';`)
	if !secretPattern.MatchString(configText) {
		t.Fatalf("config.inc.php = %q, want generated 32 character blowfish secret", configText)
	}
	for _, expected := range []string{
		"$polkaCredentialsPath = " + phpSingleQuotedString(filepath.Join(rootDir, "secrets", "db", "demo.json")) + ";",
		"$cfg['Servers'][$i]['auth_type'] = 'config';",
		"$cfg['Servers'][$i]['user'] = (string) ($polkaCredentials['user'] ?? 'polka');",
		"$cfg['Servers'][$i]['password'] = (string) ($polkaCredentials['password'] ?? '');",
		"$cfg['Servers'][$i]['host'] = '127.0.0.1';",
		"$cfg['Servers'][$i]['port'] = (string) ($polkaCredentials['port'] ?? '3307');",
		"$cfg['Servers'][$i]['pmadb'] = 'phpmyadmin';",
		"$cfg['Servers'][$i]['relation'] = 'pma__relation';",
		"$cfg['Servers'][$i]['table_info'] = 'pma__table_info';",
		"$cfg['TempDir'] = __DIR__ . '/tmp';",
	} {
		if !strings.Contains(configText, expected) {
			t.Fatalf("config.inc.php = %q, want %q", configText, expected)
		}
	}
	if _, err := os.Stat(filepath.Join(installDir, phpMyAdminTempDirectory)); err != nil {
		t.Fatalf("Stat(tmp) error = %v", err)
	}
}

func TestRenderPHPMyAdminConfigSkipsStorageWithoutManagedDatabase(t *testing.T) {
	configText := string(renderPHPMyAdminConfig("abcdefghijklmnopqrstuvwxyz123456", nil, ""))

	if strings.Contains(configText, "['pmadb']") {
		t.Fatalf("renderPHPMyAdminConfig() = %q, want storage disabled without database", configText)
	}
	if strings.Contains(configText, "$polkaCredentials") {
		t.Fatalf("renderPHPMyAdminConfig() = %q, want managed credential loading disabled without database", configText)
	}
	if !strings.Contains(configText, "$cfg['Servers'][$i]['auth_type'] = 'cookie';") {
		t.Fatalf("renderPHPMyAdminConfig() = %q, want cookie auth without managed database", configText)
	}
	if !strings.Contains(configText, "$cfg['Servers'][$i]['port'] = '3306';") {
		t.Fatalf("renderPHPMyAdminConfig() = %q, want default database port", configText)
	}
}

func TestRenderPHPMyAdminConfigSkipsManagedDatabaseForPostgreSQL(t *testing.T) {
	database := &config.DatabaseConfig{Engine: PostgreSQL, Version: "17", Port: 5432}
	credentialsPath, err := phpMyAdminManagedDatabaseCredentialsPath(t.TempDir(), "demo", database)
	if err != nil {
		t.Fatalf("phpMyAdminManagedDatabaseCredentialsPath() error = %v", err)
	}
	if credentialsPath != "" {
		t.Fatalf("phpMyAdminManagedDatabaseCredentialsPath() = %q, want empty for PostgreSQL", credentialsPath)
	}

	configText := string(renderPHPMyAdminConfig("abcdefghijklmnopqrstuvwxyz123456", database, filepath.Join("secrets", "db", "demo.json")))
	for _, unexpected := range []string{
		"$polkaCredentials",
		"['pmadb']",
		"'5432'",
	} {
		if strings.Contains(configText, unexpected) {
			t.Fatalf("renderPHPMyAdminConfig() = %q, want no PostgreSQL managed integration marker %q", configText, unexpected)
		}
	}
	if !strings.Contains(configText, "$cfg['Servers'][$i]['auth_type'] = 'cookie';") {
		t.Fatalf("renderPHPMyAdminConfig() = %q, want cookie auth for PostgreSQL", configText)
	}
	if !strings.Contains(configText, "$cfg['Servers'][$i]['port'] = '3306';") {
		t.Fatalf("renderPHPMyAdminConfig() = %q, want MySQL default port for PostgreSQL pairing", configText)
	}
}
