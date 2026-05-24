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
	Nginx         string              `yaml:"nginx,omitempty"`
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

func fakeDatabaseScript(name string) []byte {
	if runtime.GOOS == "windows" {
		return []byte("@echo off\r\necho fake-" + name + " %*\r\n")
	}

	return []byte("#!/usr/bin/env sh\nprintf 'fake-" + name + " %s\n' \"$*\"\n")
}
