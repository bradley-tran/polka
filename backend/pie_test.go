package backend

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPIEExtensionInstallArgs(t *testing.T) {
	tests := []struct {
		name     string
		phpPath  string
		pkg      string
		version  string
		expected []string
	}{
		{
			name:     "no version",
			phpPath:  "/path/to/php",
			pkg:      "foo/bar",
			version:  "",
			expected: []string{"install", "--with-php-path=/path/to/php", "--skip-enable-extension", "foo/bar"},
		},
		{
			name:     "whitespace version",
			phpPath:  "/path/to/php",
			pkg:      "foo/bar",
			version:  "  \t  ",
			expected: []string{"install", "--with-php-path=/path/to/php", "--skip-enable-extension", "foo/bar"},
		},
		{
			name:     "asterisk version",
			phpPath:  "/path/to/php",
			pkg:      "foo/bar",
			version:  "*",
			expected: []string{"install", "--with-php-path=/path/to/php", "--skip-enable-extension", "foo/bar"},
		},
		{
			name:     "specific version",
			phpPath:  "/path/to/php",
			pkg:      "foo/bar",
			version:  "^1.2",
			expected: []string{"install", "--with-php-path=/path/to/php", "--skip-enable-extension", "foo/bar:^1.2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := PIEExtensionInstallArgs(tt.phpPath, tt.pkg, tt.version)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("PIEExtensionInstallArgs() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestPIEExtensionUninstallArgs(t *testing.T) {
	result := PIEExtensionUninstallArgs("/path/to/php", "foo/bar")
	expected := []string{"uninstall", "--with-php-path=/path/to/php", "foo/bar"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("PIEExtensionUninstallArgs() = %v, want %v", result, expected)
	}
}

func TestPrepareInternalPHPCommand(t *testing.T) {
	t.Run("empty target", func(t *testing.T) {
		_, err := prepareInternalPHPCommand("  ", []string{"arg1"})
		if err == nil {
			t.Error("expected error for empty target, got nil")
		}
	})

	t.Run("normal target", func(t *testing.T) {
		cmd, err := prepareInternalPHPCommand("php", []string{"arg1", "arg2"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cmd.Path != "php" && !strings.HasSuffix(cmd.Path, "php") && !strings.HasSuffix(cmd.Path, "php.exe") {
			t.Errorf("expected path to end with php, got %v", cmd.Path)
		}
		if len(cmd.Args) < 3 {
			t.Errorf("expected args, got %v", cmd.Args)
		}
	})

	if runtime.GOOS == "windows" {
		t.Run("windows batch file", func(t *testing.T) {
			cmd, err := prepareInternalPHPCommand("php.cmd", []string{"arg1"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cmd.Path != "cmd.exe" && !strings.HasSuffix(cmd.Path, "cmd.exe") {
				t.Errorf("expected cmd.exe wrapper, got %v", cmd.Path)
			}
			expectedArgs := []string{"cmd.exe", "/c", "php.cmd", "arg1"}
			if !reflect.DeepEqual(cmd.Args, expectedArgs) {
				t.Errorf("expected args %v, got %v", expectedArgs, cmd.Args)
			}
		})
	}
}

func TestSetProcessEnvValue(t *testing.T) {
	env := []string{"VAR1=val1", "VAR2=val2"}

	// Append new key
	result1 := setProcessEnvValue(env, "VAR3", "val3")
	if !contains(result1, "VAR3=val3") {
		t.Errorf("failed to append new key: %v", result1)
	}

	// Replace existing key
	result2 := setProcessEnvValue(env, "VAR2", "newval2")
	if !contains(result2, "VAR2=newval2") || contains(result2, "VAR2=val2") {
		t.Errorf("failed to replace existing key: %v", result2)
	}

	// Bad format in existing env (should be ignored)
	envBad := []string{"VAR1=val1", "BADFORMAT"}
	result3 := setProcessEnvValue(envBad, "VAR2", "val2")
	if !contains(result3, "VAR2=val2") {
		t.Errorf("failed to handle badly formatted env var: %v", result3)
	}

	// Case insensitivity on Windows
	if runtime.GOOS == "windows" {
		result4 := setProcessEnvValue(env, "var1", "newval1")
		if !contains(result4, "var1=newval1") || contains(result4, "VAR1=val1") {
			t.Errorf("failed case-insensitive replacement on windows: %v", result4)
		}
	} else {
		result4 := setProcessEnvValue(env, "var1", "newval1")
		if !contains(result4, "var1=newval1") || !contains(result4, "VAR1=val1") {
			t.Errorf("failed case-sensitive handling on non-windows: %v", result4)
		}
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func TestInternalPHPProcessEnv(t *testing.T) {
	dir := t.TempDir()
	phpPath := filepath.Join(dir, "php")

	// Base env without files
	baseEnv := []string{"EXISTING=val"}
	env := internalPHPProcessEnv(baseEnv, phpPath)
	if !reflect.DeepEqual(env, baseEnv) {
		t.Errorf("expected unchanged env, got %v", env)
	}

	// Create php.ini
	iniPath := filepath.Join(dir, "php.ini")
	if err := os.WriteFile(iniPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	env2 := internalPHPProcessEnv(baseEnv, phpPath)
	if !contains(env2, "PHPRC="+iniPath) {
		t.Errorf("expected PHPRC to be set, got %v", env2)
	}

	// Create openssl.cnf
	sslDir := filepath.Join(dir, "extras", "ssl")
	if err := os.MkdirAll(sslDir, 0755); err != nil {
		t.Fatal(err)
	}
	sslPath := filepath.Join(sslDir, "openssl.cnf")
	if err := os.WriteFile(sslPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	env3 := internalPHPProcessEnv(baseEnv, phpPath)
	if !contains(env3, "OPENSSL_CONF="+sslPath) {
		t.Errorf("expected OPENSSL_CONF to be set, got %v", env3)
	}
}
