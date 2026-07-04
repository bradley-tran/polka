package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// setupCreateProjectCache seeds the cache with a fake internal PHP host that
// scaffolds a CakePHP-shaped app when run (standing in for php executing
// composer.phar create-project) plus a fake composer PHAR payload.
func setupCreateProjectCache(t *testing.T, workingDir string, exitCode int) {
	t.Helper()

	cacheDir := filepath.Join(t.TempDir(), "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	writeCachedPHP(t, cacheDir, "8.4", fakeCakePHPCreateProjectComposerScriptWithExit(exitCode))
	writeCachedComposer(t, cacheDir, "2.8", []byte("composer phar\n"))
	t.Setenv("POLKA_TEST_CREATE_PROJECT_DIR", filepath.Join(workingDir, "demo"))
}

// TestRunCreateProjectScaffoldsAndInitializesPolka verifies the happy path:
// internal php + composer are provisioned, the app is scaffolded, polka.yaml
// gets the detected framework preset, and the post-composer hook rewrites the
// framework's database config.
func TestRunCreateProjectScaffoldsAndInitializesPolka(t *testing.T) {
	workingDir := t.TempDir()
	setupCreateProjectCache(t, workingDir, 0)
	chdirTest(t, workingDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"create-project", "cakephp/app", "demo"}); code != 0 {
		t.Fatalf("Run(create-project) code = %d, stderr = %q", code, stderr.String())
	}

	targetDir := filepath.Join(workingDir, "demo")
	if !strings.Contains(stdout.String(), "Created cakephp project at "+targetDir) {
		t.Fatalf("Run(create-project) stdout = %q, want cakephp creation summary", stdout.String())
	}
	if !strings.Contains(stdout.String(), "polka install") {
		t.Fatalf("Run(create-project) stdout = %q, want next-step hint", stdout.String())
	}

	// polka.yaml carries the framework preset and the .polka state dir exists.
	config := readTestConfigFile(t, targetDir)
	environment := config.Environments[defaultEnvironmentName]
	if environment.Framework != "cakephp" {
		t.Fatalf("polka.yaml framework = %q, want cakephp", environment.Framework)
	}
	if environment.PHP == "" || environment.Composer == "" {
		t.Fatalf("polka.yaml tools = %#v, want framework preset versions", environment)
	}
	if _, err := os.Stat(filepath.Join(targetDir, ".polka")); err != nil {
		t.Fatalf("Stat(.polka) error = %v, want state dir initialized", err)
	}

	// The post-composer hook replaced the scaffolded SQLite datasource with
	// the managed database credentials.
	appLocal, err := os.ReadFile(filepath.Join(targetDir, "config", "app_local.php"))
	if err != nil {
		t.Fatalf("ReadFile(app_local.php) error = %v", err)
	}
	if !strings.Contains(string(appLocal), "'username' => 'polka'") {
		t.Fatalf("app_local.php = %q, want managed database credentials", string(appLocal))
	}
	if strings.Contains(string(appLocal), "Sqlite") {
		t.Fatalf("app_local.php = %q, want scaffolded SQLite datasource replaced", string(appLocal))
	}
}

// fakeFailingCreateProjectHostPHPScript builds a fake internal php that
// answers module listings normally (so internal PHP provisioning succeeds)
// but fails any other invocation, standing in for a failing composer run.
func fakeFailingCreateProjectHostPHPScript(exitCode int) []byte {
	code := strconv.Itoa(exitCode)
	if runtime.GOOS == "windows" {
		return []byte(strings.Join([]string{
			"@echo off",
			"if \"%1\"==\"-nm\" exit /b 0",
			"if \"%1\"==\"-m\" exit /b 0",
			"echo composer failure 1>&2",
			"exit /b " + code,
			"",
		}, "\r\n"))
	}

	return []byte("#!/usr/bin/env sh\ncase \"$1\" in\n-nm|-m) exit 0 ;;\nesac\necho 'composer failure' >&2\nexit " + code + "\n")
}

// TestRunCreateProjectPropagatesComposerFailure verifies a composer failure
// exits with composer's code and leaves no polka.yaml behind.
func TestRunCreateProjectPropagatesComposerFailure(t *testing.T) {
	workingDir := t.TempDir()
	cacheDir := filepath.Join(t.TempDir(), "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	writeCachedPHP(t, cacheDir, "8.4", fakeFailingCreateProjectHostPHPScript(3))
	writeCachedComposer(t, cacheDir, "2.8", []byte("composer phar\n"))
	chdirTest(t, workingDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"create-project", "cakephp/app", "demo"}); code != 3 {
		t.Fatalf("Run(create-project) code = %d, want composer exit code 3", code)
	}
	if _, err := os.Stat(filepath.Join(workingDir, "demo", "polka.yaml")); !os.IsNotExist(err) {
		t.Fatalf("Stat(polka.yaml) error = %v, want no polka init after composer failure", err)
	}
}

// TestRunCreateProjectWithoutDetectedFramework verifies unknown packages fall
// back to a plain polka init with a hint.
func TestRunCreateProjectWithoutDetectedFramework(t *testing.T) {
	workingDir := t.TempDir()
	setupCreateProjectCache(t, workingDir, 0)
	// The fake scaffolds a CakePHP layout; point the scaffold somewhere the
	// marker checks don't find framework files for the acme package.
	t.Setenv("POLKA_TEST_CREATE_PROJECT_DIR", filepath.Join(workingDir, "blog", "unrelated"))
	chdirTest(t, workingDir)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"create-project", "acme/blog"}); code != 0 {
		t.Fatalf("Run(create-project) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no framework detected") {
		t.Fatalf("Run(create-project) stdout = %q, want no-framework hint", stdout.String())
	}

	config := readTestConfigFile(t, filepath.Join(workingDir, "blog"))
	if framework := config.Environments[defaultEnvironmentName].Framework; framework != "" {
		t.Fatalf("polka.yaml framework = %q, want empty", framework)
	}
}

// TestRunCreateProjectRequiresPackage verifies the package argument is
// mandatory.
func TestRunCreateProjectRequiresPackage(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"create-project"}); code == 0 {
		t.Fatal("Run(create-project) code = 0, want failure without package")
	}
	if !strings.Contains(stderr.String(), "composer package") {
		t.Fatalf("Run(create-project) stderr = %q, want package requirement", stderr.String())
	}
}
