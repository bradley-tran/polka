package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInitUsesDotPolkaByDefault(t *testing.T) {
	projectDir := t.TempDir()
	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})

	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"init"}); code != 0 {
		t.Fatalf("Run(init) code = %d, stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "php.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/php.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "composer.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/composer.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "node.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/node.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "npm.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/npm.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "npx.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/npx.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "nodejs.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/nodejs.cmd) error = %v, want legacy nodejs shim removed", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "mago.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/mago.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "mysql.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/mysql.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "mariadb.cmd")); !os.IsNotExist(err) {
		t.Fatalf("Stat(.polka/bin/mariadb.cmd) error = %v, want missing shim without active environment", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "polka.cmd")); err != nil {
		t.Fatalf("Stat(.polka/bin/polka.cmd) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "polka.yaml")); err != nil {
		t.Fatalf("Stat(polka.yaml) error = %v", err)
	}
	if !strings.Contains(stdout.String(), filepath.Join(projectDir, ".polka")) {
		t.Fatalf("Run(init) stdout = %q, want .polka path", stdout.String())
	}
}

func TestRunInstallUsesCurrentEnvironmentWhenNameOmitted(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install"}); code != 0 {
		t.Fatalf("Run(install current) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "Installing demo environment") {
		t.Fatalf("Run(install current) stdout = %q, want current environment banner", output)
	}
	if !strings.Contains(output, "Installed 'demo' environment") {
		t.Fatalf("Run(install current) stdout = %q, want install summary", output)
	}
}

func TestRunInstallUsesConfiguredNestedRootFromProjectConfig(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, "test-site", ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}

	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    "test-site/.polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {PHP: "8.4"},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")

	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalWorkingDir)
	})
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"install"}); code != 0 {
		t.Fatalf("Run(install nested root) code = %d, stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(projectInstalledPHPPath(root, "8.4")); err != nil {
		t.Fatalf("Stat(installed php in nested root) error = %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "Installing demo environment") {
		t.Fatalf("Run(install nested root) stdout = %q, want current environment banner", output)
	}
	if !strings.Contains(output, "Installed 'demo' environment") {
		t.Fatalf("Run(install nested root) stdout = %q, want install summary", output)
	}
}

func TestRunInstallUsesConfiguredNestedRootFromExplicitRoot(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, "test-site", ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}

	writeTestConfigFile(t, projectDir, testConfigFile{
		Version: 1,
		Root:    "test-site/.polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {PHP: "8.4"},
		},
	})
	writeTestActiveEnvironment(t, root, "demo")

	if code := Run(stdout, stderr, []string{"--root", root, "install"}); code != 0 {
		t.Fatalf("Run(install nested explicit root) code = %d, stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(projectInstalledPHPPath(root, "8.4")); err != nil {
		t.Fatalf("Stat(installed php in nested root) error = %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "Installing demo environment") {
		t.Fatalf("Run(install nested explicit root) stdout = %q, want current environment banner", output)
	}
	if !strings.Contains(output, "Installed 'demo' environment") {
		t.Fatalf("Run(install nested explicit root) stdout = %q, want install summary", output)
	}
}

func TestRunConfigUsesCurrentEnvironmentWhenNameOmitted(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config demo) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use demo) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "config", "--composer", "2.8"}); code != 0 {
		t.Fatalf("Run(config current) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "Configuring demo environment") {
		t.Fatalf("Run(config current) stdout = %q, want current environment banner", output)
	}
	if !strings.Contains(output, "Configured demo") {
		t.Fatalf("Run(config current) stdout = %q, want configured summary", output)
	}

	configData, err := os.ReadFile(testEnvironmentConfigPath(projectDir, "demo"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	if environment.Composer != "2.8" {
		t.Fatalf("environment = %#v, want composer set on current environment", environment)
	}
	if strings.Contains(string(configData), "current:") {
		t.Fatalf("config = %q, want no current entry", string(configData))
	}
	if active := readTestActiveEnvironment(t, root); active != "demo" {
		t.Fatalf("active environment = %q, want demo", active)
	}
}

func TestRunConfigUsesDefaultEnvironmentWhenCurrentMissing(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config default) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "Configuring default environment") {
		t.Fatalf("Run(config default) stdout = %q, want default environment banner", output)
	}
	if !strings.Contains(output, "Configured default") {
		t.Fatalf("Run(config default) stdout = %q, want configured summary", output)
	}

	configData, err := os.ReadFile(filepath.Join(projectDir, "polka.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	if strings.Contains(string(configData), "current:") {
		t.Fatalf("config = %q, want no current entry", string(configData))
	}
	if _, err := os.Stat(filepath.Join(root, "run", "current")); !os.IsNotExist(err) {
		t.Fatalf("Stat(active environment) error = %v, want missing default fallback state", err)
	}
	environment := readTestEnvironmentConfig(t, projectDir, defaultEnvironmentName)
	if environment.PHP != "8.4" {
		t.Fatalf("environment = %#v, want php configured on default environment", environment)
	}
}

func TestRunInstallUsesDefaultEnvironmentWhenCurrentMissing(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", defaultEnvironmentName, "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config default) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install"}); code != 0 {
		t.Fatalf("Run(install default) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "Installing default environment") {
		t.Fatalf("Run(install default) stdout = %q, want default environment banner", output)
	}
	if !strings.Contains(output, "Installed 'default' environment") {
		t.Fatalf("Run(install default) stdout = %q, want install summary", output)
	}

	configData, err := os.ReadFile(filepath.Join(projectDir, "polka.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(config after install) error = %v", err)
	}
	if strings.Contains(string(configData), "current:") {
		t.Fatalf("config after install = %q, want no current entry", string(configData))
	}
	if _, err := os.Stat(filepath.Join(root, "run", "current")); !os.IsNotExist(err) {
		t.Fatalf("Stat(active environment after install) error = %v, want missing default fallback state", err)
	}
}

func TestRunNewUsesDefaultVersions(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "new", "demo"}); code != 0 {
		t.Fatalf("Run(new) code = %d, stderr = %q", code, stderr.String())
	}

	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	if environment.PHP != "8.4" || environment.Composer != "2.8" || environment.NodeJS != "24" {
		t.Fatalf("environment = %#v, want default versions for demo", environment)
	}
	if !strings.Contains(stdout.String(), "nodejs=24") {
		t.Fatalf("Run(new) stdout = %q, want default nodejs summary", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Created demo") {
		t.Fatalf("Run(new) stdout = %q, want created summary", stdout.String())
	}
}

func TestRunNewRejectsDefaultEnvironmentName(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "new", defaultEnvironmentName}); code == 0 {
		t.Fatal("Run(new default) code = 0, want reserved default rejection")
	}
	if !strings.Contains(stderr.String(), "environment \"default\" already exists") {
		t.Fatalf("Run(new default) stderr = %q, want already exists error", stderr.String())
	}
}

func TestRunConfigPersistsDatabaseSettings(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--db-engine", "mysql", "--db-version", "8.0", "--db-port", "3306"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	stored := environment.Database
	if stored == nil {
		t.Fatalf("environment = %#v, want database config for demo", environment)
	}
	if stored.Engine != "mysql" || stored.Version != "8.0" || stored.Port != 3306 {
		t.Fatalf("database = %#v, want mysql 8.0 on port 3306", stored)
	}
	configData, err := os.ReadFile(testEnvironmentConfigPath(projectDir, "demo"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	configText := string(configData)
	if !strings.Contains(configText, "tools:\n  mysql: \"8.0\"") {
		t.Fatalf("config = %q, want mysql version under tools", configText)
	}
	if !strings.Contains(configText, "database:\n  engine: mysql\n  port: 3306") {
		t.Fatalf("config = %q, want root-level database engine and port", configText)
	}
	if strings.Contains(configText, "  version:") {
		t.Fatalf("config = %q, want no root-level database version entry", configText)
	}
	if !strings.Contains(stdout.String(), "db=mysql:8.0@3306") {
		t.Fatalf("Run(config) stdout = %q, want database summary", stdout.String())
	}
}

func TestRunConfigPersistsNodeJSSetting(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--nodejs", "24"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	if environment.NodeJS != "24" {
		t.Fatalf("environment = %#v, want nodejs configured for demo", environment)
	}
	if !strings.Contains(stdout.String(), "nodejs=24") {
		t.Fatalf("Run(config) stdout = %q, want nodejs summary", stdout.String())
	}
}

func TestRunStatusShowsToolsEachOnOwnLine(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:        "8.4",
				Composer:   "2.8",
				NodeJS:     "24",
				Mago:       "1.27",
				Nginx:      "1.30",
				HTTPS:      true,
				PHPMyAdmin: &testPHPMyAdminConfig{Version: "5.2", Port: 8082},
				Database:   &testDatabaseConfig{Engine: "mysql", Version: "8.0", Port: 3306},
				Server:     &testServerConfig{Hostname: "localhost", Port: 8080},
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")

	if code := Run(stdout, stderr, []string{"--root", root, "status"}); code != 0 {
		t.Fatalf("Run(status) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	for _, expected := range []string{
		"environment demo\n",
		"php 8.4\n",
		"composer 2.8\n",
		"nodejs 24\n",
		"mago 1.27\n",
		"nginx 1.30\n",
		"phpmyadmin 5.2 ui=https://127.0.0.1:8082\n",
		"database mysql:8.0@3306\n",
		"mailpit unset\n",
		"server https://localhost:8080\n",
		"webserver stopped\n",
		"phpmyadmin-server stopped\n",
		"database-server stopped\n",
		"mailpit-server unset\n",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("Run(status) stdout = %q, want %q", output, expected)
		}
	}
}

func TestRunInfoAliasShowsStatus(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP: "8.4",
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")

	if code := Run(stdout, stderr, []string{"--root", root, "info"}); code != 0 {
		t.Fatalf("Run(info) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "environment demo\n") {
		t.Fatalf("Run(info) stdout = %q, want status output", stdout.String())
	}
}

func TestRunStatusUsesDefaultServerAddress(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "status"}); code != 0 {
		t.Fatalf("Run(status) code = %d, stderr = %q", code, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "server http://localhost:8000\n") {
		t.Fatalf("Run(status) stdout = %q, want default server address", output)
	}
	if !strings.Contains(output, "nodejs unset\n") {
		t.Fatalf("Run(status) stdout = %q, want nodejs unset line", output)
	}
	if !strings.Contains(output, "mago unset\n") {
		t.Fatalf("Run(status) stdout = %q, want mago unset line", output)
	}
	if !strings.Contains(output, "nginx unset\n") {
		t.Fatalf("Run(status) stdout = %q, want nginx unset line", output)
	}
	if !strings.Contains(output, "phpmyadmin unset\n") {
		t.Fatalf("Run(status) stdout = %q, want phpmyadmin unset line", output)
	}
	if !strings.Contains(output, "webserver stopped\n") {
		t.Fatalf("Run(status) stdout = %q, want webserver stopped line", output)
	}
	if !strings.Contains(output, "phpmyadmin-server unset\n") {
		t.Fatalf("Run(status) stdout = %q, want phpmyadmin server unset line", output)
	}
	if !strings.Contains(output, "database-server unset\n") {
		t.Fatalf("Run(status) stdout = %q, want database unset line", output)
	}
	if !strings.Contains(output, "mailpit unset\n") || !strings.Contains(output, "mailpit-server unset\n") {
		t.Fatalf("Run(status) stdout = %q, want mailpit unset lines", output)
	}
}

func TestRunStatusShowsRunningWebserverAndDatabase(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:      "8.4",
				Database: &testDatabaseConfig{Engine: "mysql", Version: "8.0", Port: 3307},
				Server:   &testServerConfig{Hostname: "localhost", Port: 8080},
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")
	if err := writeServeState(serveStatePath(root, "demo"), serveRuntimeState{
		EnvironmentName: "demo",
		ServerKind:      "php",
		ServerScheme:    "http",
		ServerAddress:   "localhost:8080",
		Docroot:         filepath.Join(projectDir, "site", "public"),
		PrimaryPID:      1010,
	}); err != nil {
		t.Fatalf("writeServeState() error = %v", err)
	}
	if err := writeDatabaseState(databaseStatePath(root, "demo"), dbRuntimeState{
		EnvironmentName: "demo",
		Engine:          "mysql",
		Version:         "8.0",
		Port:            3307,
		PID:             2020,
	}); err != nil {
		t.Fatalf("writeDatabaseState() error = %v", err)
	}

	oldPingServe := pingServeAddressFunc
	oldPingDatabase := pingDatabaseAddressFunc
	t.Cleanup(func() {
		pingServeAddressFunc = oldPingServe
		pingDatabaseAddressFunc = oldPingDatabase
	})
	pingServeAddressFunc = func(address string) bool {
		return address == "localhost:8080"
	}
	pingDatabaseAddressFunc = func(address string) bool {
		return address == databaseAddress(3307)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "status"}); code != 0 {
		t.Fatalf("Run(status) code = %d, stderr = %q", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "webserver running http://localhost:8080\n") {
		t.Fatalf("Run(status) stdout = %q, want running webserver line", output)
	}
	if !strings.Contains(output, "database-server running mysql:8.0@3307\n") {
		t.Fatalf("Run(status) stdout = %q, want running database line", output)
	}
}

func TestRunStatusShowsMailpitUIURL(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	config := testConfigFile{
		Version: 1,
		Root:    ".polka",
		Environments: map[string]testEnvironmentConfig{
			"demo": {
				PHP:     "8.4",
				HTTPS:   true,
				Mailpit: &testMailpitConfig{Version: "1.30", SMTPPort: 1125, UIPort: 8125},
			},
		},
	}
	writeTestConfigFile(t, projectDir, config)
	writeTestActiveEnvironment(t, root, "demo")
	if err := writeMailpitState(mailpitStatePath(root, "demo"), mailpitRuntimeState{
		EnvironmentName: "demo",
		Version:         "1.30",
		SMTPPort:        1125,
		UIPort:          8125,
		UIScheme:        "https",
		PID:             5656,
	}); err != nil {
		t.Fatalf("writeMailpitState() error = %v", err)
	}

	oldPingMailpit := pingMailpitAddressFunc
	t.Cleanup(func() {
		pingMailpitAddressFunc = oldPingMailpit
	})
	pingMailpitAddressFunc = func(address string) bool {
		return address == "127.0.0.1:1125" || address == "127.0.0.1:8125"
	}

	if code := Run(stdout, stderr, []string{"--root", root, "status"}); code != 0 {
		t.Fatalf("Run(status) code = %d, stderr = %q", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "mailpit 1.30 smtp=1125 ui=https://127.0.0.1:8125\n") {
		t.Fatalf("Run(status) stdout = %q, want configured mailpit UI URL", output)
	}
	if !strings.Contains(output, "mailpit-server running smtp=1125 ui=https://127.0.0.1:8125\n") {
		t.Fatalf("Run(status) stdout = %q, want running mailpit UI URL", output)
	}
}

func TestRunInstallAppliesPHPExtensionsFromConfigFile(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cacheDir, "php", "8.4", "ext"), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache ext) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	environment.PHPExtensions = map[string]bool{"openssl": true, "xdebug": false}
	writeTestEnvironmentConfig(t, projectDir, "demo", environment)

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	phpIniData, err := os.ReadFile(projectInstalledPHPConfigPath(root, "8.4"))
	if err != nil {
		t.Fatalf("ReadFile(installed php.ini) error = %v", err)
	}
	phpIni := string(phpIniData)
	if !strings.Contains(phpIni, "extension=openssl") {
		t.Fatalf("php.ini = %q, want enabled openssl extension", phpIni)
	}
	if !strings.Contains(phpIni, ";extension=xdebug") {
		t.Fatalf("php.ini = %q, want disabled xdebug extension", phpIni)
	}
	if !strings.Contains(stdout.String(), "Installed 'demo' environment") {
		t.Fatalf("Run(install) stdout = %q, want install summary", stdout.String())
	}
}

func TestRunInstallEnablesComposerPHPExtensionsByDefault(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakePHP := cachedPHPPath(cacheDir, "8.4")
	if err := os.MkdirAll(filepath.Dir(fakePHP), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache php) error = %v", err)
	}
	if err := os.WriteFile(fakePHP, fakePHPScript(), 0o755); err != nil {
		t.Fatalf("WriteFile(cache php) error = %v", err)
	}
	fakeComposer := cachedComposerPath(cacheDir, "2.8")
	if err := os.MkdirAll(filepath.Dir(fakeComposer), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache composer) error = %v", err)
	}
	if err := os.WriteFile(fakeComposer, []byte("composer\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(cache composer) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cacheDir, "php", "8.4", "ext"), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache ext) error = %v", err)
	}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "demo", "--php", "8.4", "--composer", "2.8"}); code != 0 {
		t.Fatalf("Run(config) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}

	phpIniData, err := os.ReadFile(projectInstalledPHPConfigPath(root, "8.4"))
	if err != nil {
		t.Fatalf("ReadFile(installed php.ini) error = %v", err)
	}
	phpIni := string(phpIniData)
	if !strings.Contains(phpIni, "extension=openssl") {
		t.Fatalf("php.ini = %q, want enabled openssl extension", phpIni)
	}
	if !strings.Contains(phpIni, "extension=zip") {
		t.Fatalf("php.ini = %q, want enabled zip extension", phpIni)
	}
}
