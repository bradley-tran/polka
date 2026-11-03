package cli

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// trustStoreCall records the exact executable, arguments, and interactive sink
// without starting any privileged process.
type trustStoreCall struct {
	name   string
	args   []string
	output io.Writer
}

// linuxTrustStoreFixture supplies fake distro metadata and commands. Its only
// file mutations are simulated install/rm operations inside a temporary directory.
type linuxTrustStoreFixture struct {
	system      linuxTrustStoreSystem
	anchors     string
	release     string
	uid         int
	interactive bool
	calls       []trustStoreCall
	lookups     []string
	failCommand string
}

// newLinuxTrustStoreFixture isolates every host-facing operation, including
// filesystem probes during preparation and sudo authentication.
func newLinuxTrustStoreFixture(t *testing.T, release string) *linuxTrustStoreFixture {
	t.Helper()
	fixture := &linuxTrustStoreFixture{anchors: t.TempDir(), release: release}
	fixture.system = linuxTrustStoreSystem{
		readFile: func(path string) ([]byte, error) {
			if path == "/etc/os-release" {
				return []byte(fixture.release), nil
			}
			return os.ReadFile(path)
		},
		stat:  func(string) (os.FileInfo, error) { return os.Stat(fixture.anchors) },
		lstat: os.Lstat,
		commandPath: func(name string) (string, error) {
			fixture.lookups = append(fixture.lookups, name)
			return "/system/" + name, nil
		},
		euid:        func() int { return fixture.uid },
		interactive: func() bool { return fixture.interactive },
		run: func(name string, args []string, output io.Writer) error {
			fixture.calls = append(fixture.calls, trustStoreCall{name, append([]string(nil), args...), output})
			if name == "/system/sudo" {
				if reflect.DeepEqual(args, []string{"-v"}) || reflect.DeepEqual(args, []string{"-n", "-v"}) {
					if fixture.failCommand == "sudo" {
						return errors.New("sudo: authentication denied")
					}
					return nil
				}
				if len(args) < 3 || args[0] != "-n" || args[1] != "--" {
					t.Fatalf("unexpected sudo arguments: %q", args)
				}
				name, args = args[2], args[3:]
			}
			if name == "/system/"+fixture.failCommand {
				return errors.New("native command failed: permission denied")
			}
			switch name {
			case "/system/install", "/system/rm":
				target := args[len(args)-1]
				if filepath.Dir(target) != fixture.anchors {
					t.Fatalf("fake command targeted a host path: %s", target)
				}
				if name == "/system/rm" {
					if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
						return err
					}
					return nil
				}
				data, err := os.ReadFile(args[len(args)-2])
				if err != nil {
					return err
				}
				return os.WriteFile(target, data, 0o644)
			}
			return nil
		},
	}
	return fixture
}

// prepare routes the fixture's managed entries into its temporary directory
// after verifying the real distro-specific layout selected by preparation.
func (fixture *linuxTrustStoreFixture) prepare(t *testing.T) *linuxTrustStore {
	t.Helper()
	store, err := prepareLinuxTrustStore(fixture.system, io.Discard)
	if err != nil {
		t.Fatalf("prepareLinuxTrustStore() error = %v", err)
	}
	store.backend.anchorDir = fixture.anchors
	return store
}

// TestDetectLinuxTrustStore covers native layouts, derivatives, identifier
// precedence, quoting, and the distinct Debian and Alpine removal behavior.
func TestDetectLinuxTrustStore(t *testing.T) {
	for _, test := range []struct {
		release string
		dir     string
		command string
		refresh []string
		remove  []string
	}{
		{release: "ID=debian", dir: "/usr/local/share/ca-certificates", command: "update-ca-certificates", remove: []string{"--fresh"}},
		{release: "# comment\nID=\"ubuntu\"", dir: "/usr/local/share/ca-certificates", command: "update-ca-certificates", remove: []string{"--fresh"}},
		{release: "ID=linuxmint\nID_LIKE='ubuntu debian'", dir: "/usr/local/share/ca-certificates", command: "update-ca-certificates", remove: []string{"--fresh"}},
		{release: "ID=fedora", dir: "/etc/pki/ca-trust/source/anchors", command: "update-ca-trust", refresh: []string{"extract"}, remove: []string{"extract"}},
		{release: "ID=rhel", dir: "/etc/pki/ca-trust/source/anchors", command: "update-ca-trust", refresh: []string{"extract"}, remove: []string{"extract"}},
		{release: "ID=rocky\nID_LIKE=\"rhel centos fedora\"", dir: "/etc/pki/ca-trust/source/anchors", command: "update-ca-trust", refresh: []string{"extract"}, remove: []string{"extract"}},
		{release: "ID=arch", dir: "/etc/ca-certificates/trust-source/anchors", command: "update-ca-trust", refresh: []string{"extract"}, remove: []string{"extract"}},
		{release: "ID=cachyos\nID_LIKE=arch", dir: "/etc/ca-certificates/trust-source/anchors", command: "update-ca-trust", refresh: []string{"extract"}, remove: []string{"extract"}},
		{release: "ID=opensuse-tumbleweed\nID_LIKE=\"opensuse suse\"", dir: "/etc/pki/trust/anchors", command: "update-ca-certificates"},
		{release: "ID=sles", dir: "/etc/pki/trust/anchors", command: "update-ca-certificates"},
		{release: "ID=custom\nID_LIKE=\"unknown suse debian\"", dir: "/etc/pki/trust/anchors", command: "update-ca-certificates"},
		{release: "ID=alpine\nID_LIKE=debian", dir: "/usr/local/share/ca-certificates", command: "update-ca-certificates"},
		{release: "ID=custom\nID_LIKE=\"rhel debian\"", dir: "/etc/pki/ca-trust/source/anchors", command: "update-ca-trust", refresh: []string{"extract"}, remove: []string{"extract"}},
	} {
		t.Run(test.release, func(t *testing.T) {
			backend, err := detectLinuxTrustStore([]byte(test.release))
			if err != nil {
				t.Fatalf("detectLinuxTrustStore() error = %v", err)
			}
			if backend.anchorDir != test.dir || backend.refreshCommand != test.command || !reflect.DeepEqual(backend.refreshArgs, test.refresh) || !reflect.DeepEqual(backend.removeArgs, test.remove) {
				t.Errorf("backend = %+v, want directory %s and command %s %v / %v", backend, test.dir, test.command, test.refresh, test.remove)
			}
		})
	}
	for _, release := range []string{"", "ID=nixos", "ID=\"debian", "ID='debian", strings.Repeat("x", 70_000)} {
		if _, err := detectLinuxTrustStore([]byte(release)); err == nil {
			t.Errorf("detectLinuxTrustStore(%q) succeeded, want error", release[:min(len(release), 30)])
		}
	}
}

// TestPrepareLinuxTrustStorePrivileges verifies authentication mode and reuse
// of the same elevated runner, without elevating Polka or prompting in CI.
func TestPrepareLinuxTrustStorePrivileges(t *testing.T) {
	caPath, _ := generateTrustStoreCA(t)
	for _, test := range []struct {
		name        string
		uid         int
		interactive bool
		auth        []string
	}{
		{name: "root"},
		{name: "interactive user", uid: 1000, interactive: true, auth: []string{"-v"}},
		{name: "noninteractive user", uid: 1000, auth: []string{"-n", "-v"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLinuxTrustStoreFixture(t, "ID=arch")
			fixture.uid, fixture.interactive = test.uid, test.interactive
			var stderr bytes.Buffer
			store, err := prepareLinuxTrustStore(fixture.system, &stderr)
			if err != nil {
				t.Fatalf("prepareLinuxTrustStore() error = %v", err)
			}
			store.backend.anchorDir = fixture.anchors
			if test.uid != 0 {
				if len(fixture.calls) != 1 || fixture.calls[0].name != "/system/sudo" || !reflect.DeepEqual(fixture.calls[0].args, test.auth) {
					t.Fatalf("authentication calls = %+v, want sudo %q", fixture.calls, test.auth)
				}
				if (fixture.calls[0].output == &stderr) != test.interactive {
					t.Errorf("authentication output = %v, interactive = %v", fixture.calls[0].output, test.interactive)
				}
			} else if len(fixture.calls) != 0 || slices.Contains(fixture.lookups, "sudo") {
				t.Fatalf("root preparation used sudo: %+v, lookups %q", fixture.calls, fixture.lookups)
			}
			fixture.calls = nil
			if _, err := store.install(caPath); err != nil {
				t.Fatalf("install() error = %v", err)
			}
			if len(fixture.calls) != 2 {
				t.Fatalf("install calls = %+v, want copy and refresh", fixture.calls)
			}
			for _, call := range fixture.calls {
				if test.uid != 0 && (call.name != "/system/sudo" || len(call.args) < 3 || call.args[0] != "-n" || call.args[1] != "--") {
					t.Errorf("privileged call = %+v, want sudo -n -- command", call)
				}
				if call.output != nil {
					t.Errorf("native operation requested interactive output: %+v", call)
				}
			}
		})
	}
}

// TestPrepareLinuxTrustStoreFailures checks that every missing prerequisite
// fails during preparation, before any native file or refresh command runs.
func TestPrepareLinuxTrustStoreFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*linuxTrustStoreFixture)
		want   string
	}{
		{name: "missing release", change: func(f *linuxTrustStoreFixture) {
			f.system.readFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }
		}, want: "distribution information"},
		{name: "release permission", change: func(f *linuxTrustStoreFixture) {
			f.system.readFile = func(path string) ([]byte, error) {
				if path != "/etc/os-release" {
					return nil, errors.New("unexpected release fallback")
				}
				return nil, os.ErrPermission
			}
		}, want: "permission denied"},
		{name: "unsupported distro", change: func(f *linuxTrustStoreFixture) { f.release = "ID=nixos" }, want: "not supported"},
		{name: "malformed release", change: func(f *linuxTrustStoreFixture) { f.release = "ID=\"arch" }, want: "parse os-release"},
		{name: "missing layout", change: func(f *linuxTrustStoreFixture) {
			f.system.stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
		}, want: "install ca-certificates-utils"},
		{name: "invalid layout", change: func(f *linuxTrustStoreFixture) {
			path := filepath.Join(f.anchors, "file")
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			f.system.stat = func(string) (os.FileInfo, error) { return os.Stat(path) }
		}, want: "not a directory"},
		{name: "sudo authentication", change: func(f *linuxTrustStoreFixture) { f.uid, f.failCommand = 1000, "sudo" }, want: "authentication denied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLinuxTrustStoreFixture(t, "ID=arch")
			test.change(fixture)
			if _, err := prepareLinuxTrustStore(fixture.system, io.Discard); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("prepareLinuxTrustStore() error = %v, want %q", err, test.want)
			}
			for _, call := range fixture.calls {
				if call.name != "/system/sudo" || !reflect.DeepEqual(call.args, []string{"-n", "-v"}) {
					t.Errorf("preparation attempted a mutating command: %+v", call)
				}
			}
		})
	}
	for _, missing := range []string{"update-ca-trust", "install", "rm", "sudo"} {
		t.Run("missing "+missing, func(t *testing.T) {
			fixture := newLinuxTrustStoreFixture(t, "ID=arch")
			fixture.uid = 1000
			fixture.system.commandPath = func(name string) (string, error) {
				if name == missing {
					return "", os.ErrNotExist
				}
				return "/system/" + name, nil
			}
			if _, err := prepareLinuxTrustStore(fixture.system, io.Discard); err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("prepareLinuxTrustStore() error = %v, want missing %s", err, missing)
			}
			if len(fixture.calls) != 0 {
				t.Errorf("preparation ran commands with missing prerequisites: %+v", fixture.calls)
			}
		})
	}
}

// TestPrepareLinuxTrustStoreReleaseFallback uses the vendor release only when
// /etc/os-release is absent, and verifies the selected native layout is probed.
func TestPrepareLinuxTrustStoreReleaseFallback(t *testing.T) {
	fixture := newLinuxTrustStoreFixture(t, "")
	var paths []string
	fixture.system.readFile = func(path string) ([]byte, error) {
		paths = append(paths, path)
		if path == "/etc/os-release" {
			return nil, os.ErrNotExist
		}
		return []byte("ID=fedora"), nil
	}
	fixture.system.stat = func(path string) (os.FileInfo, error) {
		if path != "/etc/pki/ca-trust/source" {
			t.Errorf("layout path = %s, want Fedora layout", path)
		}
		return os.Stat(fixture.anchors)
	}
	if _, err := prepareLinuxTrustStore(fixture.system, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"/etc/os-release", "/usr/lib/os-release"}) {
		t.Errorf("release reads = %q", paths)
	}
}

// generateTrustStoreCA uses Polka's actual generator and keeps both the CA and
// its private keys entirely inside a temporary cache.
func generateTrustStoreCA(t *testing.T) (string, *x509.Certificate) {
	t.Helper()
	cache := t.TempDir()
	if _, _, err := regenerateGlobalTLSCertificateWithOptions(cache, tlsCAKeyStorageOptions{Policy: tlsCAKeyStoragePlaintextRequired}); err != nil {
		t.Fatal(err)
	}
	path, _ := globalTLSCACertificatePaths(cache)
	certificate, err := readCertificateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, certificate
}

// TestLinuxTrustStoreRotation checks exact native commands, repeat installation,
// independent CA entries, and removal refresh semantics for every backend.
func TestLinuxTrustStoreRotation(t *testing.T) {
	caPath, ca := generateTrustStoreCA(t)
	otherPath, otherCA := generateTrustStoreCA(t)
	for _, id := range []string{"debian", "fedora", "arch", "opensuse-leap", "alpine"} {
		t.Run(id, func(t *testing.T) {
			fixture := newLinuxTrustStoreFixture(t, "ID="+id)
			store := fixture.prepare(t)
			target := filepath.Join(fixture.anchors, fmt.Sprintf("polka-local-ca-%x.crt", sha256.Sum256(ca.Raw)))
			if store.certificatePath(ca) != target {
				t.Fatalf("CA path = %s, want %s", store.certificatePath(ca), target)
			}
			name, err := store.install(caPath)
			if err != nil || !strings.Contains(name, "Linux system") {
				t.Fatalf("install() = %q, %v", name, err)
			}
			want := []trustStoreCall{
				{name: "/system/install", args: []string{"-D", "-m", "0644", "--", caPath, target}},
				{name: "/system/" + store.backend.refreshCommand, args: store.backend.refreshArgs},
			}
			if !reflect.DeepEqual(fixture.calls, want) {
				t.Fatalf("install commands = %+v, want %+v", fixture.calls, want)
			}
			if _, err := store.install(caPath); err != nil {
				t.Fatalf("repeat install: %v", err)
			}
			if _, err := store.install(otherPath); err != nil {
				t.Fatalf("independent CA install: %v", err)
			}
			fixture.calls = nil
			if _, err := store.remove(ca); err != nil {
				t.Fatalf("remove: %v", err)
			}
			want = []trustStoreCall{
				{name: "/system/rm", args: []string{"-f", "--", target}},
				{name: "/system/" + store.backend.refreshCommand, args: store.backend.removeArgs},
			}
			if !reflect.DeepEqual(fixture.calls, want) {
				t.Fatalf("removal commands = %+v, want %+v", fixture.calls, want)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Errorf("retired CA still exists: %v", err)
			}
			if _, err := os.Stat(store.certificatePath(otherCA)); err != nil {
				t.Errorf("independent CA was removed: %v", err)
			}
			fixture.calls = nil
			if _, err := store.remove(ca); err != nil || len(fixture.calls) != 0 {
				t.Errorf("absent CA removal: error %v, commands %+v", err, fixture.calls)
			}
		})
	}
}

// TestLinuxTrustStoreRejectsConflicts checks that neither install nor removal
// replaces unexpected contents or follows symlinks at a managed filename.
func TestLinuxTrustStoreRejectsConflicts(t *testing.T) {
	caPath, ca := generateTrustStoreCA(t)
	otherPath, _ := generateTrustStoreCA(t)
	for _, kind := range []string{"malformed", "different CA", "directory", "symlink", "bundle"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "symlink" && runtime.GOOS == "windows" {
				t.Skip("creating Windows symlinks requires additional privileges")
			}
			fixture := newLinuxTrustStoreFixture(t, "ID=arch")
			store := fixture.prepare(t)
			target := store.certificatePath(ca)
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(target, 0o755)
			case "symlink":
				err = os.Symlink(caPath, target)
			default:
				data := []byte("malformed certificate")
				if kind == "different CA" {
					data, err = os.ReadFile(otherPath)
				} else if kind == "bundle" {
					data, err = os.ReadFile(caPath)
					data = append(data, data...)
				}
				if err == nil {
					err = os.WriteFile(target, data, 0o644)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.install(caPath); err == nil || !strings.Contains(err.Error(), target) {
				t.Errorf("install conflict error = %v, want managed path", err)
			}
			if _, err := store.remove(ca); err == nil || !strings.Contains(err.Error(), target) {
				t.Errorf("remove conflict error = %v, want managed path", err)
			}
			if len(fixture.calls) != 0 {
				t.Errorf("conflict ran native commands: %+v", fixture.calls)
			}
		})
	}
}

// TestLinuxTrustStoreInvalidCertificates rejects input that is not exactly one
// public CA and handles read/inspection failures before native commands run.
func TestLinuxTrustStoreInvalidCertificates(t *testing.T) {
	caPath, ca := generateTrustStoreCA(t)
	for _, kind := range []string{"missing", "invalid PEM", "invalid DER", "preamble", "server certificate", "private key", "read denied", "inspect denied"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newLinuxTrustStoreFixture(t, "ID=debian")
			store := fixture.prepare(t)
			path := filepath.Join(t.TempDir(), "invalid.crt")
			var data []byte
			switch kind {
			case "invalid PEM":
				data = []byte("not PEM")
			case "invalid DER":
				data = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("invalid DER")})
			case "preamble":
				certificateData, err := os.ReadFile(caPath)
				if err != nil {
					t.Fatal(err)
				}
				data = append([]byte("unexpected preamble\n"), certificateData...)
			case "server certificate":
				path = filepath.Join(filepath.Dir(caPath), serveTLSCertFileName)
			case "private key":
				path = filepath.Join(filepath.Dir(caPath), serveTLSCAKeyName)
			case "read denied":
				store.system.readFile = func(string) ([]byte, error) { return nil, os.ErrPermission }
			case "inspect denied":
				path = caPath
				store.system.lstat = func(string) (os.FileInfo, error) { return nil, os.ErrPermission }
			}
			if data != nil {
				if err := os.WriteFile(path, data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.install(path); err == nil {
				t.Error("install succeeded for invalid input")
			}
			if kind == "inspect denied" {
				if _, err := store.remove(ca); err == nil {
					t.Error("remove succeeded after inspection failure")
				}
			}
			if len(fixture.calls) != 0 {
				t.Errorf("invalid input ran native commands: %+v", fixture.calls)
			}
		})
	}
	store := newLinuxTrustStoreFixture(t, "ID=arch").prepare(t)
	if _, err := store.remove(nil); err == nil {
		t.Error("remove(nil) succeeded")
	}
}

// TestLinuxTrustStoreCommandFailures verifies that failed native operations
// surface their diagnostics and target paths without claiming success.
func TestLinuxTrustStoreCommandFailures(t *testing.T) {
	caPath, ca := generateTrustStoreCA(t)
	for _, phase := range []string{"install", "install refresh", "remove", "remove refresh"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newLinuxTrustStoreFixture(t, "ID=arch")
			store := fixture.prepare(t)
			if strings.HasPrefix(phase, "remove") {
				if _, err := store.install(caPath); err != nil {
					t.Fatal(err)
				}
			}
			fixture.calls = nil
			fixture.failCommand = "update-ca-trust"
			if phase == "install" {
				fixture.failCommand = "install"
			} else if phase == "remove" {
				fixture.failCommand = "rm"
			}
			var err error
			var name string
			if strings.HasPrefix(phase, "remove") {
				name, err = store.remove(ca)
			} else {
				name, err = store.install(caPath)
			}
			if err == nil || name != "" || !strings.Contains(err.Error(), "permission denied") || !strings.Contains(err.Error(), store.certificatePath(ca)) {
				t.Errorf("%s = %q, %v, want diagnostic and target path", phase, name, err)
			}
			wantCalls := 1
			if strings.HasSuffix(phase, "refresh") {
				wantCalls = 2
			}
			if len(fixture.calls) != wantCalls {
				t.Errorf("failure commands = %+v, want %d", fixture.calls, wantCalls)
			}
			if _, err := os.Stat(caPath); err != nil {
				t.Errorf("cached CA lost on failure: %v", err)
			}
		})
	}
}

// TestLinuxTrustStoreCommandProcess is a subprocess fixture for testing the
// real command runner's combined diagnostics and interactive output forwarding.
func TestLinuxTrustStoreCommandProcess(t *testing.T) {
	if os.Getenv("POLKA_TEST_TRUST_COMMAND") != "1" {
		return
	}
	if _, err := fmt.Fprintln(os.Stdout, "native stdout"); err != nil {
		os.Exit(2)
	}
	if _, err := fmt.Fprintln(os.Stderr, "native stderr"); err != nil {
		os.Exit(2)
	}
	os.Exit(7)
}

// TestRunLinuxTrustStoreCommand checks the actual process boundary using only
// a test subprocess, including errors when a resolved executable disappears.
func TestRunLinuxTrustStoreCommand(t *testing.T) {
	t.Setenv("POLKA_TEST_TRUST_COMMAND", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, interactive := range []bool{false, true} {
		var output bytes.Buffer
		var writer io.Writer
		if interactive {
			writer = &output
		}
		err := runLinuxTrustStoreCommand(executable, []string{"-test.run=^TestLinuxTrustStoreCommandProcess$"}, writer)
		if err == nil || !strings.Contains(err.Error(), "native stdout") || !strings.Contains(err.Error(), "native stderr") {
			t.Errorf("run error = %v, want combined diagnostics", err)
		}
		if interactive != strings.Contains(output.String(), "native stderr") {
			t.Errorf("interactive = %v, forwarded output = %q", interactive, output.String())
		}
	}
	if err := runLinuxTrustStoreCommand(filepath.Join(t.TempDir(), "missing-command"), nil, nil); err == nil {
		t.Error("missing executable succeeded")
	}
	if err := runLinuxTrustStoreCommand(executable, []string{"-test.run=^$"}, nil); err != nil {
		t.Errorf("successful subprocess: %v", err)
	}
}

// TestLinuxTrustStoreCommandPath verifies that a user-controlled PATH cannot
// supply a privileged executable and that ordinary system tools still resolve.
func TestLinuxTrustStoreCommandPath(t *testing.T) {
	projectBin := t.TempDir()
	name := "polka-trust-test-command"
	if err := os.WriteFile(filepath.Join(projectBin, name), []byte("test command"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", projectBin)
	if _, err := linuxTrustStoreCommandPath(name); err == nil {
		t.Error("resolved a command from the user's PATH")
	}
	if runtime.GOOS == "linux" {
		path, err := linuxTrustStoreCommandPath("rm")
		if err != nil || !filepath.IsAbs(path) || filepath.Dir(path) == projectBin {
			t.Errorf("system command path = %q, %v", path, err)
		}
	}
}
