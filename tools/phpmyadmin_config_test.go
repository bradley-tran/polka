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
	indexPath := filepath.Join(installDir, "index.php")
	if err := os.WriteFile(indexPath, []byte("<?php\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(index.php) error = %v", err)
	}

	err := configureInstalledPHPMyAdmin(InstallContext{
		Environment: config.Environment{
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
		"$cfg['Servers'][$i]['host'] = '127.0.0.1';",
		"$cfg['Servers'][$i]['port'] = '3307';",
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
	configText := string(renderPHPMyAdminConfig("abcdefghijklmnopqrstuvwxyz123456", nil))

	if strings.Contains(configText, "['pmadb']") {
		t.Fatalf("renderPHPMyAdminConfig() = %q, want storage disabled without database", configText)
	}
	if !strings.Contains(configText, "$cfg['Servers'][$i]['port'] = '3306';") {
		t.Fatalf("renderPHPMyAdminConfig() = %q, want default database port", configText)
	}
}
