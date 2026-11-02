package cli

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBuildIntegration compiles real tools from upstream sources and uses them the
// way a project does. It needs a C toolchain, host libraries, network access, and
// several minutes, so it only runs when asked:
//
//	POLKA_BUILD_INTEGRATION=1 go test ./cli -run TestBuildIntegration -v -timeout 40m
//
// POLKA_BUILD_INTEGRATION_CACHE reuses a cache between runs, and
// POLKA_BUILD_INTEGRATION_PHP_ARGS holds space-separated --configure-arg values for
// PHP, such as --without-tidy on a host without that library.
func TestBuildIntegration(t *testing.T) {
	if os.Getenv("POLKA_BUILD_INTEGRATION") != "1" {
		t.Skip("set POLKA_BUILD_INTEGRATION=1 to compile real tools")
	}

	cacheDir := os.Getenv("POLKA_BUILD_INTEGRATION_CACHE")
	if cacheDir == "" {
		cacheDir = filepath.Join(t.TempDir(), "cache")
	}
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	t.Setenv("POLKA_TOOLS_DIR", filepath.Join(t.TempDir(), "internal-tools"))
	// PIE keeps a working copy per target PHP under the user's home, and these
	// targets are temporary directories that would otherwise leave clutter there.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	projectDir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(projectDir, "public"), 0o755); err != nil {
		t.Fatalf("MkdirAll(public) error = %v", err)
	}
	index := `<?php echo "served:", PHP_SAPI, ":", extension_loaded("mbstring") ? "mbstring" : "none";`
	if err := os.WriteFile(filepath.Join(projectDir, "public", "index.php"), []byte(index), 0o644); err != nil {
		t.Fatalf("WriteFile(index.php) error = %v", err)
	}
	t.Chdir(projectDir)

	runIntegrationCommand(t, "init")

	phpArgs := []string{}
	for _, arg := range strings.Fields(os.Getenv("POLKA_BUILD_INTEGRATION_PHP_ARGS")) {
		phpArgs = append(phpArgs, "--configure-arg="+arg)
	}
	runIntegrationCommand(t, append([]string{"build", "php:8.4"}, phpArgs...)...)
	runIntegrationCommand(t, "build", "nginx:1.30")
	runIntegrationCommand(t, "build", "apache:2.4")
	runIntegrationCommand(t, "build", "redis:8.2")

	t.Run("webservers serve PHP and stop cleanly", func(t *testing.T) {
		for _, server := range []struct{ kind, version string }{{"nginx", "1.30"}, {"apache", "2.4"}} {
			port := freeIntegrationPort(t)
			writeIntegrationConfig(t, projectDir, server.kind, server.version, port)
			runIntegrationCommand(t, "install")
			runIntegrationCommand(t, "serve")

			body := getIntegrationPage(t, fmt.Sprintf("http://127.0.0.1:%d/index.php", port))
			if body != "served:cgi-fcgi:mbstring" {
				runIntegrationCommand(t, "stop")
				t.Fatalf("%s served %q", server.kind, body)
			}

			// A server that leaves workers behind fails here: stop waits for the
			// port, and the listener check confirms nothing still holds it.
			runIntegrationCommand(t, "stop")
			if connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
				_ = connection.Close()
				t.Fatalf("%s left port %d listening after stop", server.kind, port)
			}
		}
	})

	phpDir := filepath.Join(projectDir, ".polka", "envs", "php", "8.4")
	php := filepath.Join(phpDir, "bin", "php")

	t.Run("every built extension loads from another directory", func(t *testing.T) {
		modules, err := filepath.Glob(filepath.Join(phpDir, "ext", "*.so"))
		if err != nil {
			t.Fatalf("Glob() error = %v", err)
		}
		if len(modules) == 0 {
			t.Fatal("no shared extensions were built")
		}
		// opcache gets no configure flag, so confirm it still arrives as a module on
		// 8.4; from PHP 8.5 it is built in and this file does not exist.
		if _, err := os.Stat(filepath.Join(phpDir, "ext", "opcache.so")); err != nil {
			t.Fatalf("Stat(opcache.so) error = %v", err)
		}
		for _, module := range modules {
			name := strings.TrimSuffix(filepath.Base(module), ".so")
			directive := "extension"
			if name == "opcache" {
				directive = "zend_extension"
			}
			output := runIntegrationProcess(t, os.TempDir(), php, "-n", "-d", "extension_dir="+filepath.Join(phpDir, "ext"), "-d", directive+"="+name, "-r", `echo "loaded";`)
			if output != "loaded" {
				t.Fatalf("loading %s printed %q", name, output)
			}
		}
	})

	t.Run("php-config reports the relocated install", func(t *testing.T) {
		phpConfig := filepath.Join(phpDir, "bin", "php-config")
		for flag, want := range map[string]string{
			"--prefix":        phpDir,
			"--extension-dir": filepath.Join(phpDir, "ext"),
			"--php-binary":    php,
		} {
			resolved, err := filepath.EvalSymlinks(want)
			if err != nil {
				resolved = want
			}
			if got := runIntegrationProcess(t, os.TempDir(), phpConfig, flag); got != want && got != resolved {
				t.Fatalf("php-config %s = %q, want %q", flag, got, want)
			}
		}
	})

	t.Run("PIE installs and removes an extension", func(t *testing.T) {
		port := freeIntegrationPort(t)
		writeIntegrationConfig(t, projectDir, "nginx", "1.30", port)
		runIntegrationCommand(t, "ext", "install", "apcu/apcu")
		// PHP runs directly with its generated ini, because `polka exec` goes through
		// a shim that re-executes the current binary, which here is the test binary.
		probe := `echo extension_loaded("apcu") ? "yes" : "no";`
		iniPath := filepath.Join(phpDir, "bin", "php.ini")
		if got := runIntegrationProcess(t, os.TempDir(), php, "-c", iniPath, "-r", probe); got != "yes" {
			t.Fatalf("apcu loaded = %q after ext install", got)
		}
		runIntegrationCommand(t, "ext", "remove", "apcu/apcu")
		if _, err := os.Stat(filepath.Join(phpDir, "ext", "apcu.so")); !os.IsNotExist(err) {
			t.Fatalf("Stat(apcu.so) error = %v, want removed", err)
		}
	})
}

// runIntegrationCommand runs polka and fails the test with its output on error.
func runIntegrationCommand(t *testing.T, args ...string) {
	t.Helper()
	runIntegrationCaptured(t, args...)
}

// runIntegrationCaptured runs polka with output in a file, since commands that
// start a child hand it their standard streams, and returns its trimmed stdout.
func runIntegrationCaptured(t *testing.T, args ...string) string {
	t.Helper()
	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	defer stdoutFile.Close()
	stderr := &bytes.Buffer{}

	if code := Run(stdoutFile, stderr, args); code != 0 {
		data, readErr := os.ReadFile(stdoutFile.Name())
		t.Fatalf("polka %s exit = %d (read error %v)\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, readErr, data, stderr.String())
	}
	data, err := os.ReadFile(stdoutFile.Name())
	if err != nil {
		t.Fatalf("ReadFile(stdout) error = %v", err)
	}

	return strings.TrimSpace(string(data))
}

// runIntegrationProcess runs a payload binary directly from dir.
func runIntegrationProcess(t *testing.T, dir, target string, args ...string) string {
	t.Helper()
	command := exec.Command(target, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v error = %v, output = %s", target, args, err, output)
	}

	return strings.TrimSpace(string(output))
}

// writeIntegrationConfig writes a project using the given webserver and port.
func writeIntegrationConfig(t *testing.T, projectDir, server, version string, port int) {
	t.Helper()
	config := fmt.Sprintf(`version: 0.2
root: .polka
docroot: public
tools:
  php: "8.4"
  %[1]s: "%[2]s"
  redis: "8.2"
php-extensions:
  mbstring: true
https: false
server:
  type: %[1]s
  hostname: localhost
  port: %[3]d
`, server, version, port)
	if err := os.WriteFile(filepath.Join(projectDir, "polka.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("WriteFile(polka.yaml) error = %v", err)
	}
}

// freeIntegrationPort reserves an unused local port.
func freeIntegrationPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port
}

// getIntegrationPage fetches a page, retrying while the server finishes starting.
func getIntegrationPage(t *testing.T, url string) string {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	var lastErr error
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		response, err := client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read %s: %v, close: %v", url, readErr, closeErr)
		}
		return strings.TrimSpace(string(body))
	}
	t.Fatalf("GET %s error = %v", url, lastErr)

	return ""
}
