package backend

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
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
			// The platform is fixed because PIE's target flags differ by OS;
			// TestPiePHPTargetArgs covers the other branch.
			result := pieExtensionInstallArgs("windows", tt.phpPath, tt.pkg, tt.version)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("pieExtensionInstallArgs() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestPIEExtensionUninstallArgs(t *testing.T) {
	result := pieExtensionUninstallArgs("windows", "/path/to/php", "foo/bar")
	expected := []string{"uninstall", "--with-php-path=/path/to/php", "foo/bar"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("pieExtensionUninstallArgs() = %v, want %v", result, expected)
	}
}

// TestPiePHPTargetArgs pins the platform split: PIE refuses --with-php-path off
// Windows, and without an explicit phpize it would find a distribution's on PATH.
func TestPiePHPTargetArgs(t *testing.T) {
	phpPath := filepath.Join("install", "bin", "php")
	want := []string{
		"--with-php-config=" + filepath.Join("install", "bin", "php-config"),
		"--with-phpize-path=" + filepath.Join("install", "bin", "phpize"),
	}
	if got := piePHPTargetArgs("linux", phpPath); !reflect.DeepEqual(got, want) {
		t.Errorf("piePHPTargetArgs(linux) = %v, want %v", got, want)
	}

	wantInstall := append(append([]string{"install"}, want...), "--skip-enable-extension", "foo/bar")
	if got := pieExtensionInstallArgs("linux", phpPath, "foo/bar", ""); !reflect.DeepEqual(got, wantInstall) {
		t.Errorf("pieExtensionInstallArgs(linux) = %v, want %v", got, wantInstall)
	}
	wantUninstall := append(append([]string{"uninstall"}, want...), "foo/bar")
	if got := pieExtensionUninstallArgs("linux", phpPath, "foo/bar"); !reflect.DeepEqual(got, wantUninstall) {
		t.Errorf("pieExtensionUninstallArgs(linux) = %v, want %v", got, wantUninstall)
	}
}

// TestPieProcessConfigSeparatesInternalAndTargetIni verifies that on Linux the
// internal PHP keeps its ini through -c while PHPRC names the target's ini, so the
// target PIE inspects reports its own extension_dir rather than the internal one.
func TestPieProcessConfigSeparatesInternalAndTargetIni(t *testing.T) {
	internalDir := t.TempDir()
	targetDir := t.TempDir()
	internalIni := filepath.Join(internalDir, "php.ini")
	targetIni := filepath.Join(targetDir, "php.ini")
	for _, path := range []string{internalIni, targetIni} {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}
	internalPHP := filepath.Join(internalDir, "php")
	targetPHP := filepath.Join(targetDir, "php")

	args, env := pieProcessConfig("linux", nil, internalPHP, "pie.phar", targetPHP, []string{"install"})
	if want := []string{"-c", internalIni, "pie.phar", "install"}; !reflect.DeepEqual(args, want) {
		t.Errorf("linux args = %v, want %v", args, want)
	}
	if want := "PHPRC=" + targetIni; !slices.Contains(env, want) {
		t.Errorf("linux env = %v, want %s", env, want)
	}

	args, env = pieProcessConfig("windows", nil, internalPHP, "pie.phar", targetPHP, []string{"install"})
	if want := []string{"pie.phar", "install"}; !reflect.DeepEqual(args, want) {
		t.Errorf("windows args = %v, want %v", args, want)
	}
	if want := "PHPRC=" + internalIni; !slices.Contains(env, want) {
		t.Errorf("windows env = %v, want %s", env, want)
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
