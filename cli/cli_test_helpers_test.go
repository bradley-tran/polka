package cli

import (
	"path/filepath"
	"runtime"
)

type testConfigFile struct {
	Version      int                              `yaml:"version,omitempty"`
	Root         string                           `yaml:"root,omitempty"`
	Current      string                           `yaml:"current,omitempty"`
	Environments map[string]testEnvironmentConfig `yaml:"environments"`
}

type testEnvironmentConfig struct {
	PHP           string              `yaml:"php"`
	Composer      string              `yaml:"composer"`
	NodeJS        string              `yaml:"nodejs,omitempty"`
	Nginx         string              `yaml:"nginx,omitempty"`
	Docroot       string              `yaml:"docroot,omitempty"`
	EnvFile       string              `yaml:"env-file,omitempty"`
	EnvVars       map[string]string   `yaml:"env-vars,omitempty"`
	Database      *testDatabaseConfig `yaml:"database,omitempty"`
	PHPExtensions map[string]bool     `yaml:"php-extensions,omitempty"`
	Server        *testServerConfig   `yaml:"server,omitempty"`
}

type testDatabaseConfig struct {
	Engine  string `yaml:"engine,omitempty"`
	Version string `yaml:"version,omitempty"`
	Port    int    `yaml:"port,omitempty"`
}

type testServerConfig struct {
	Hostname string `yaml:"hostname,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	HTTPS    bool   `yaml:"https,omitempty"`
}

func cachedPHPPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "php", version, "bin", "php.cmd")
	}

	return filepath.Join(root, "php", version, "bin", "php")
}

func cachedComposerPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "composer", version, "bin", "composer.cmd")
	}

	return filepath.Join(root, "composer", version, "bin", "composer.phar")
}

func cachedNodeJSPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "nodejs", version, "node.exe")
	}

	return filepath.Join(root, "nodejs", version, "bin", "node")
}

func projectInstalledNodeJSCommandPath(root, version, command string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "envs", "nodejs", version, command+".cmd")
	}

	return filepath.Join(root, "envs", "nodejs", version, "bin", command)
}

func projectInstalledNodeJSPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "envs", "nodejs", version, "node.exe")
	}

	return filepath.Join(root, "envs", "nodejs", version, "bin", "node")
}

func cachedDatabasePath(root, tool, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, tool, version, "bin", tool+".cmd")
	}

	return filepath.Join(root, tool, version, "bin", tool)
}

func cachedDatabaseServerPath(root, tool, version string) string {
	serverName := "mysqld"
	if tool == "mariadb" {
		serverName = "mariadbd"
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(root, tool, version, "bin", serverName+".exe")
	}

	return filepath.Join(root, tool, version, "bin", serverName)
}

func cachedDatabaseAdminPath(root, tool, version string) string {
	adminName := "mysqladmin"
	if tool == "mariadb" {
		adminName = "mariadb-admin"
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(root, tool, version, "bin", adminName+".exe")
	}

	return filepath.Join(root, tool, version, "bin", adminName)
}

func cachedDatabaseDumpPath(root, tool, version string) string {
	dumpName := "mysqldump"
	if tool == "mariadb" {
		dumpName = "mariadb-dump"
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(root, tool, version, "bin", dumpName+".cmd")
	}

	return filepath.Join(root, tool, version, "bin", dumpName)
}

func projectInstalledPHPPath(root, version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "envs", "php", version, "bin", "php.cmd")
	}

	return filepath.Join(root, "envs", "php", version, "bin", "php")
}

func projectInstalledPHPConfigPath(root, version string) string {
	return filepath.Join(filepath.Dir(projectInstalledPHPPath(root, version)), "php.ini")
}

func fakePHPScript() []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho fake-php %*\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf 'fake-php %s\n' \"$*\"\n")
}

func fakePHPScriptWithEnv(varName string) []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho fake-php %* %" + varName + "%\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf 'fake-php %s %s\n' \"$*\" \"$" + varName + "\"\n")
}

func fakeToolScript(name string) []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho fake-" + name + " %*\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf 'fake-" + name + " %s\n' \"$*\"\n")
}

func fakeDatabaseScript(name string) []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho fake-" + name + " %*\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf 'fake-" + name + " %s\n' \"$*\"\n")
}

func fakeDatabaseCaptureScript(name string) []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho fake-" + name + " %*\r\npowershell -NoProfile -Command \"[IO.File]::WriteAllText($env:POLKA_TEST_DB_CAPTURE_PATH, [Console]::In.ReadToEnd())\"\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf 'fake-" + name + " %s\n' \"$*\"\ncat > \"$POLKA_TEST_DB_CAPTURE_PATH\"\n")
}

func fakeDatabaseDumpScript() []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\nif not \"%POLKA_TEST_DB_DUMP_CAPTURE_PATH%\"==\"\" echo %* > \"%POLKA_TEST_DB_DUMP_CAPTURE_PATH%\"\r\npowershell -NoProfile -Command \"[Console]::Out.Write($env:POLKA_TEST_DB_DUMP_OUTPUT)\"\r\n")
	}

	return []byte("#!/usr/bin/env sh\nif [ -n \"$POLKA_TEST_DB_DUMP_CAPTURE_PATH\" ]; then\n  printf '%s\n' \"$*\" > \"$POLKA_TEST_DB_DUMP_CAPTURE_PATH\"\nfi\nprintf '%s' \"$POLKA_TEST_DB_DUMP_OUTPUT\"\n")
}
