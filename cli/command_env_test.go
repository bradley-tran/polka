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
	chdirTest(t, projectDir)

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
	environment := readTestEnvironmentConfig(t, projectDir, defaultEnvironmentName)
	if !environment.HTTPS || environment.Server == nil || environment.Server.Hostname != strings.ToLower(filepath.Base(projectDir))+".localhost" {
		t.Fatalf("environment = %#v, want https and project-local hostname", environment)
	}
	if !strings.Contains(stdout.String(), filepath.Join(projectDir, ".polka")) {
		t.Fatalf("Run(init) stdout = %q, want .polka path", stdout.String())
	}
}

func TestRunInitUsesCurrentDirectoryWithoutParentDiscovery(t *testing.T) {
	parentDir := t.TempDir()
	projectDir := filepath.Join(parentDir, "site")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(project) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(parentDir, "polka.yaml"), []byte("version: 1\nroot: .polka\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(parent config) error = %v", err)
	}
	chdirTest(t, projectDir)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"init"}); code != 0 {
		t.Fatalf("Run(init) code = %d, stderr = %q", code, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(projectDir, "polka.yaml")); err != nil {
		t.Fatalf("Stat(nested polka.yaml) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".polka", "bin", "polka.cmd")); err != nil {
		t.Fatalf("Stat(nested dispatcher) error = %v", err)
	}
	environment := readTestEnvironmentConfig(t, projectDir, defaultEnvironmentName)
	if !environment.HTTPS || environment.Server == nil || environment.Server.Hostname != "site.localhost" {
		t.Fatalf("environment = %#v, want nested https and site.localhost hostname", environment)
	}
	if _, err := os.Stat(filepath.Join(parentDir, ".polka")); !os.IsNotExist(err) {
		t.Fatalf("Stat(parent .polka) error = %v, want parent project untouched", err)
	}
	if !strings.Contains(stdout.String(), filepath.Join(projectDir, ".polka")) {
		t.Fatalf("Run(init) stdout = %q, want nested .polka path", stdout.String())
	}
}

func TestRunInitWithFrameworkWritesDefaultPreset(t *testing.T) {
	for _, test := range []struct {
		framework       string
		docroot         string
		wantComposer    string
		wantNodeJS      string
		wantMailpit     bool
		wantMailpitShim bool
	}{
		{framework: "cakephp", docroot: "webroot", wantComposer: "2.8", wantNodeJS: "24", wantMailpit: true, wantMailpitShim: true},
		{framework: "codeigniter", docroot: "public", wantComposer: "2.8", wantNodeJS: "24", wantMailpit: true, wantMailpitShim: true},
		{framework: "drupal", docroot: "web", wantComposer: "2.8", wantNodeJS: "24", wantMailpit: true, wantMailpitShim: true},
		{framework: "laravel", docroot: "public", wantComposer: "2.8", wantNodeJS: "24", wantMailpit: true, wantMailpitShim: true},
		{framework: "symfony", docroot: "public", wantComposer: "2.8", wantNodeJS: "24", wantMailpit: true, wantMailpitShim: true},
		{framework: "wordpress", docroot: ".", wantComposer: "", wantNodeJS: "", wantMailpit: false, wantMailpitShim: false},
	} {
		t.Run(test.framework, func(t *testing.T) {
			projectDir := t.TempDir()
			root := filepath.Join(projectDir, ".polka")
			chdirTest(t, projectDir)
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			if code := Run(stdout, stderr, []string{"--root", root, "init", test.framework}); code != 0 {
				t.Fatalf("Run(init %s) code = %d, stderr = %q", test.framework, code, stderr.String())
			}

			environment := readTestEnvironmentConfig(t, projectDir, defaultEnvironmentName)
			if environment.Framework != test.framework || environment.Docroot != test.docroot {
				t.Fatalf("environment = %#v, want framework/docroot preset", environment)
			}
			if !environment.HTTPS || environment.Server == nil || environment.Server.Hostname != strings.ToLower(filepath.Base(projectDir))+".localhost" {
				t.Fatalf("environment = %#v, want framework preset with https and project-local hostname", environment)
			}
			if environment.PHP != "8.4" || environment.Composer != test.wantComposer || environment.NodeJS != test.wantNodeJS || environment.Nginx != "1.30" || environment.MariaDB != "11.8" {
				t.Fatalf("environment = %#v, want framework tool preset", environment)
			}
			if environment.Database == nil || environment.Database.Engine != "mariadb" || environment.Database.Version != "11.8" || environment.Database.Port != 3306 {
				t.Fatalf("database = %#v, want MariaDB preset", environment.Database)
			}
			if environment.PHPMyAdmin == nil || environment.PHPMyAdmin.Version != "5.2" || environment.PHPMyAdmin.Port != 8082 {
				t.Fatalf("phpmyadmin = %#v, want phpMyAdmin preset", environment.PHPMyAdmin)
			}
			if (environment.Mailpit != nil) != test.wantMailpit {
				t.Fatalf("mailpit = %#v, want presence %v", environment.Mailpit, test.wantMailpit)
			}
			if test.wantMailpit && (environment.Mailpit.Version != "1.30" || environment.Mailpit.SMTPPort != 1025 || environment.Mailpit.UIPort != 8025) {
				t.Fatalf("mailpit = %#v, want Mailpit preset", environment.Mailpit)
			}
			if environment.OPcachePreset != "dev" {
				t.Fatalf("opcache-preset = %q, want dev", environment.OPcachePreset)
			}
			if test.framework == "drupal" {
				if environment.OPcacheConfig["opcache.save_comments"] != "1" {
					t.Fatalf("opcache-config = %#v, want Drupal save_comments preset", environment.OPcacheConfig)
				}
			} else if len(environment.OPcacheConfig) != 0 {
				t.Fatalf("opcache-config = %#v, want no framework-specific OPcache config", environment.OPcacheConfig)
			}
			if len(environment.PHPExtensions) != 0 {
				t.Fatalf("php-extensions = %#v, want framework init to omit default extensions", environment.PHPExtensions)
			}
			configData, err := os.ReadFile(filepath.Join(projectDir, "polka.yaml"))
			if err != nil {
				t.Fatalf("ReadFile(polka.yaml) error = %v", err)
			}
			if strings.Contains(string(configData), "php-extensions:") {
				t.Fatalf("polka.yaml = %q, want no default php-extensions block", string(configData))
			}
			if _, err := os.Stat(filepath.Join(root, "bin", "php.cmd")); err != nil {
				t.Fatalf("Stat(php shim) error = %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "bin", "nginx.cmd")); err != nil {
				t.Fatalf("Stat(nginx shim) error = %v", err)
			}
			_, mailpitErr := os.Stat(filepath.Join(root, "bin", "mailpit.cmd"))
			if test.wantMailpitShim && mailpitErr != nil {
				t.Fatalf("Stat(mailpit shim) error = %v", mailpitErr)
			}
			if !test.wantMailpitShim && !os.IsNotExist(mailpitErr) {
				t.Fatalf("Stat(mailpit shim) error = %v, want missing shim", mailpitErr)
			}
			if !strings.Contains(stdout.String(), "Initialized Polka "+test.framework+" project") {
				t.Fatalf("Run(init %s) stdout = %q, want framework init summary", test.framework, stdout.String())
			}
		})
	}
}

func TestRunInitWithFrameworkDocrootOverride(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	chdirTest(t, projectDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "init", "drupal", "--docroot", "drupal/web"}); code != 0 {
		t.Fatalf("Run(init drupal --docroot) code = %d, stderr = %q", code, stderr.String())
	}

	environment := readTestEnvironmentConfig(t, projectDir, defaultEnvironmentName)
	if environment.Framework != "drupal" || environment.Docroot != "drupal/web" {
		t.Fatalf("environment = %#v, want drupal framework with overridden docroot", environment)
	}
	if environment.Composer != "2.8" || environment.NodeJS != "24" || environment.Nginx != "1.30" {
		t.Fatalf("environment = %#v, want other Drupal preset values preserved", environment)
	}
}

func TestRunInitWritesDocrootWithoutFramework(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	chdirTest(t, projectDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "init", "--docroot", "public"}); code != 0 {
		t.Fatalf("Run(init --docroot) code = %d, stderr = %q", code, stderr.String())
	}

	environment := readTestEnvironmentConfig(t, projectDir, defaultEnvironmentName)
	if environment.Docroot != "public" {
		t.Fatalf("environment = %#v, want docroot override", environment)
	}
}

func TestRunInitRejectsEmptyDocrootOverride(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	chdirTest(t, projectDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "init", "drupal", "--docroot", "   "}); code == 0 {
		t.Fatal("Run(init drupal --docroot blank) code = 0, want failure")
	}
	if !strings.Contains(stderr.String(), "--docroot requires a non-empty value") {
		t.Fatalf("Run(init drupal --docroot blank) stderr = %q, want non-empty error", stderr.String())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("Stat(root) error = %v, want no root created before failure", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "polka.yaml")); !os.IsNotExist(err) {
		t.Fatalf("Stat(polka.yaml) error = %v, want no config created before failure", err)
	}
}

func TestRunInitWithFrameworkRejectsExistingConfigBeforeMutation(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	chdirTest(t, projectDir)
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), []byte("version: 1\nroot: .polka\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "init", "drupal"}); code == 0 {
		t.Fatal("Run(init drupal existing config) code = 0, want failure")
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("Run(init drupal existing config) stderr = %q, want existing config error", stderr.String())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("Stat(root) error = %v, want no root created before failure", err)
	}
}

func TestRunInitWithFrameworkRejectsUnknownFramework(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	chdirTest(t, projectDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "init", "yii"}); code == 0 {
		t.Fatal("Run(init yii) code = 0, want unsupported framework failure")
	}
	if !strings.Contains(stderr.String(), "unsupported framework") || !strings.Contains(stderr.String(), "cakephp, codeigniter, drupal, laravel, symfony, wordpress") {
		t.Fatalf("Run(init yii) stderr = %q, want supported framework list", stderr.String())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("Stat(root) error = %v, want no root created before failure", err)
	}
}

func TestRunInstallUsesCurrentEnvironmentWhenNameOmitted(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")

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

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())

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

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())

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

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use demo) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "config", "tools.composer", "2.8"}); code != 0 {
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

	if code := Run(stdout, stderr, []string{"--root", root, "config", "tools.php", "8.4"}); code != 0 {
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

func TestRunConfigEnvDefaultIgnoresCurrentEnvironment(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")
	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "use", "demo"}); code != 0 {
		t.Fatalf("Run(use demo) code = %d, stderr = %q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "config", "--env", defaultEnvironmentName, "tools.composer", "2.8"}); code != 0 {
		t.Fatalf("Run(config --env default) code = %d, stderr = %q", code, stderr.String())
	}

	defaultEnvironment := readTestEnvironmentConfig(t, projectDir, defaultEnvironmentName)
	if defaultEnvironment.Composer != "2.8" {
		t.Fatalf("default environment = %#v, want composer configured", defaultEnvironment)
	}
	demoEnvironment := readTestEnvironmentConfig(t, projectDir, "demo")
	if demoEnvironment.Composer != "" {
		t.Fatalf("demo environment = %#v, want composer untouched", demoEnvironment)
	}
	if active := readTestActiveEnvironment(t, root); active != "demo" {
		t.Fatalf("active environment = %q, want demo", active)
	}
}

func TestRunConfigPersistsSchemaDotKeys(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")
	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.mailpit", "1.30")
	runTestConfigValue(t, stdout, stderr, root, "demo", "settings.mailpit.smtp-port", "1125")
	runTestConfigValue(t, stdout, stderr, root, "demo", "settings.mailpit.ui-port", "8125")
	runTestConfigValue(t, stdout, stderr, root, "demo", "env-vars.APP_ENV", "local")
	runTestConfigValue(t, stdout, stderr, root, "demo", "php-extensions.xdebug", "false")
	runTestConfigValue(t, stdout, stderr, root, "demo", "opcache-config.opcache.enable_cli", "1")

	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	if environment.Mailpit == nil || environment.Mailpit.Version != "1.30" || environment.Mailpit.SMTPPort != 1125 || environment.Mailpit.UIPort != 8125 {
		t.Fatalf("mailpit = %#v, want version and configured ports", environment.Mailpit)
	}
	if environment.EnvVars["APP_ENV"] != "local" {
		t.Fatalf("env-vars = %#v, want APP_ENV", environment.EnvVars)
	}
	if environment.PHPExtensions["xdebug"] {
		t.Fatalf("php-extensions = %#v, want xdebug disabled", environment.PHPExtensions)
	}
	if environment.OPcacheConfig["opcache.enable_cli"] != "1" {
		t.Fatalf("opcache-config = %#v, want dotted directive", environment.OPcacheConfig)
	}
}

func TestRunConfigRejectsRemovedFlags(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "--php", "8.4"}); code == 0 {
		t.Fatal("Run(config --php) code = 0, want removed flag error")
	}
	if !strings.Contains(stderr.String(), "unknown flag: --php") {
		t.Fatalf("Run(config --php) stderr = %q, want unknown flag error", stderr.String())
	}
}

func TestRunConfigRejectsInvalidKeysAndValuesWithoutWriting(t *testing.T) {
	testCases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "metadata root", args: []string{"--env", "demo", "root", ".polka"}, wantErr: "unsupported config key"},
		{name: "whole object", args: []string{"--env", "demo", "tools", "php"}, wantErr: "unsupported config key"},
		{name: "invalid port", args: []string{"--env", "demo", "database.port", "nope"}, wantErr: "database.port requires an integer value"},
		{name: "orphan setting", args: []string{"--env", "demo", "settings.mailpit.smtp-port", "1025"}, wantErr: "mailpit configuration requires version"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			projectDir := t.TempDir()
			root := filepath.Join(projectDir, ".polka")
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			args := []string{"--root", root, "config"}
			args = append(args, testCase.args...)
			if code := Run(stdout, stderr, args); code == 0 {
				t.Fatalf("Run(config %v) code = 0, want failure", testCase.args)
			}
			if !strings.Contains(stderr.String(), testCase.wantErr) {
				t.Fatalf("Run(config %v) stderr = %q, want %q", testCase.args, stderr.String(), testCase.wantErr)
			}
			if _, err := os.Stat(testEnvironmentConfigPath(projectDir, "demo")); !os.IsNotExist(err) {
				t.Fatalf("Stat(demo config) error = %v, want no named config written", err)
			}
		})
	}
}

func TestRunConfigDoesNotWriteInvalidUpdate(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")
	configPath := testEnvironmentConfigPath(projectDir, "demo")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config before invalid update) error = %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "config", "--env", "demo", "tools.php", "bad version!"}); code == 0 {
		t.Fatal("Run(config invalid version) code = 0, want failure")
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config after invalid update) error = %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("config after invalid update = %q, want unchanged %q", string(after), string(before))
	}
}

func TestRunInstallUsesDefaultEnvironmentWhenCurrentMissing(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())

	runTestConfigValue(t, stdout, stderr, root, defaultEnvironmentName, "tools.php", "8.4")

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

func TestRunInstallAcceptsExplicitToolVersion(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())

	if code := Run(stdout, stderr, []string{"--root", root, "install", "php:8.4"}); code != 0 {
		t.Fatalf("Run(install php:8.4) code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(projectInstalledPHPPath(root, "8.4")); err != nil {
		t.Fatalf("Stat(installed php) error = %v", err)
	}
	environment := readTestEnvironmentConfig(t, projectDir, defaultEnvironmentName)
	if environment.PHP != "8.4" {
		t.Fatalf("default environment = %#v, want php 8.4 persisted", environment)
	}
	output := stdout.String()
	if !strings.Contains(output, "Installed php:8.4 for 'default' environment") {
		t.Fatalf("Run(install php:8.4) stdout = %q, want explicit install summary", output)
	}
	if !strings.Contains(output, "php 8.4\t(cached)") {
		t.Fatalf("Run(install php:8.4) stdout = %q, want cached tool result", output)
	}
}

func TestRunInstallUsesEnvFlagForNamedEnvironment(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install --env demo) code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(projectInstalledPHPPath(root, "8.4")); err != nil {
		t.Fatalf("Stat(installed php) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "Installed 'demo' environment") {
		t.Fatalf("Run(install --env demo) stdout = %q, want install summary", stdout.String())
	}
}

func TestRunInstallRejectsEnvironmentNameArgument(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "install", "demo"}); code == 0 {
		t.Fatal("Run(install demo) code = 0, want tool version parse error")
	}
	if !strings.Contains(stderr.String(), "install argument must be TOOL:VERSION") {
		t.Fatalf("Run(install demo) stderr = %q, want tool version parse error", stderr.String())
	}
	if !strings.Contains(stderr.String(), "use --env NAME to select an environment") {
		t.Fatalf("Run(install demo) stderr = %q, want --env guidance", stderr.String())
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

	runTestMySQLConfig(t, stdout, stderr, root, "demo", "8.0", 3306)

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
	if !strings.Contains(stdout.String(), "database.port=3306") {
		t.Fatalf("Run(config) stdout = %q, want database port summary", stdout.String())
	}
}

func TestRunConfigPersistsNodeJSSetting(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.nodejs", "24")

	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	if environment.NodeJS != "24" {
		t.Fatalf("environment = %#v, want nodejs configured for demo", environment)
	}
	if !strings.Contains(stdout.String(), "tools.nodejs=24") {
		t.Fatalf("Run(config) stdout = %q, want nodejs summary", stdout.String())
	}
}

func TestRunConfigPersistsPIESetting(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.pie", "1.4")

	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	if environment.PIE != "1.4" {
		t.Fatalf("environment = %#v, want pie configured for demo", environment)
	}
	if !strings.Contains(stdout.String(), "tools.pie=1.4") {
		t.Fatalf("Run(config) stdout = %q, want pie summary", stdout.String())
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
				PIE:        "1.4",
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
		"pie 1.4\n",
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

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")
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
	if !strings.Contains(output, "pie unset\n") {
		t.Fatalf("Run(status) stdout = %q, want pie unset line", output)
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

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())
	if err := os.MkdirAll(filepath.Join(cacheDir, "php", "8.4", "ext"), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache ext) error = %v", err)
	}

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")

	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	environment.PHPExtensions = map[string]bool{"openssl": true, "xdebug": false}
	writeTestEnvironmentConfig(t, projectDir, "demo", environment)

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
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
	if !strings.Contains(phpIni, "curl.cainfo=") || !strings.Contains(phpIni, "openssl.cafile=") {
		t.Fatalf("php.ini = %q, want TLS CA bundle directives", phpIni)
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

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())
	writeCachedComposer(t, cacheDir, "2.8", []byte("composer\n"))
	if err := os.MkdirAll(filepath.Join(cacheDir, "php", "8.4", "ext"), 0o755); err != nil {
		t.Fatalf("MkdirAll(cache ext) error = %v", err)
	}

	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.php", "8.4")
	runTestConfigValue(t, stdout, stderr, root, "demo", "tools.composer", "2.8")

	stdout.Reset()
	stderr.Reset()

	if code := Run(stdout, stderr, []string{"--root", root, "install", "--env", "demo"}); code != 0 {
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
	if !strings.Contains(phpIni, "curl.cainfo=") || !strings.Contains(phpIni, "openssl.cafile=") {
		t.Fatalf("php.ini = %q, want TLS CA bundle directives", phpIni)
	}
	if !strings.Contains(phpIni, "extension=zip") {
		t.Fatalf("php.ini = %q, want enabled zip extension", phpIni)
	}
}
