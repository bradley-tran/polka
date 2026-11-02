package tools

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildTestPrefix stands in for the path compiled into a built tool.
const buildTestPrefix = "/opt/polka/build/demo"

// writePayloadFile creates a file under root with the given mode.
func writePayloadFile(t *testing.T, root, relative, contents string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

// readPayloadEntries returns an archive's headers keyed by entry name.
func readPayloadEntries(t *testing.T, archivePath string) map[string]*tar.Header {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("Open(archive) error = %v", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	reader := tar.NewReader(gzipReader)

	entries := map[string]*tar.Header{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		entries[header.Name] = header
	}
}

// skipWithoutSymlinks skips where the test process cannot create symlinks, which
// is the default for unprivileged Windows users.
func skipWithoutSymlinks(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// TestPackBuildPayloadRoundTrip verifies a payload survives what install does to
// it: extraction plus flattening. A bin-only tree keeps its bin directory, which is
// the case the wrapping top-level directory exists for.
func TestPackBuildPayloadRoundTrip(t *testing.T) {
	payloadDir := t.TempDir()
	writePayloadFile(t, payloadDir, "bin/tool", "#!/bin/sh\n", 0o755)
	if err := os.Mkdir(filepath.Join(payloadDir, "logs"), 0o755); err != nil {
		t.Fatalf("Mkdir(logs) error = %v", err)
	}
	// Only bin and logs exist at the root; removing logs makes bin the sole entry.
	archivePath := filepath.Join(t.TempDir(), "payload.tar.gz")
	if err := packBuildPayload(payloadDir, "demo-1.0", buildTestPrefix, archivePath); err != nil {
		t.Fatalf("packBuildPayload() error = %v", err)
	}

	installDir := t.TempDir()
	if err := extractArchive(archivePath, installDir, archiveFormatTarGz); err != nil {
		t.Fatalf("extractArchive() error = %v", err)
	}
	if err := collapseSingleDirectory(installDir); err != nil {
		t.Fatalf("collapseSingleDirectory() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "bin", "tool")); err != nil {
		t.Fatalf("Stat(bin/tool) error = %v", err)
	}
	// An empty directory the tool expects at runtime must survive packaging.
	if info, err := os.Stat(filepath.Join(installDir, "logs")); err != nil || !info.IsDir() {
		t.Fatalf("Stat(logs) info = %v, error = %v", info, err)
	}
}

// TestPackBuildPayloadKeepsBinOnlyTree covers the flattening case directly: with
// bin as the only entry, the wrapper is what is removed, not bin.
func TestPackBuildPayloadKeepsBinOnlyTree(t *testing.T) {
	payloadDir := t.TempDir()
	writePayloadFile(t, payloadDir, "bin/tool", "x", 0o755)
	archivePath := filepath.Join(t.TempDir(), "payload.tar.gz")
	if err := packBuildPayload(payloadDir, "demo-1.0", buildTestPrefix, archivePath); err != nil {
		t.Fatalf("packBuildPayload() error = %v", err)
	}

	installDir := t.TempDir()
	if err := extractArchive(archivePath, installDir, archiveFormatTarGz); err != nil {
		t.Fatalf("extractArchive() error = %v", err)
	}
	if err := collapseSingleDirectory(installDir); err != nil {
		t.Fatalf("collapseSingleDirectory() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "bin", "tool")); err != nil {
		t.Fatalf("Stat(bin/tool) error = %v, want bin kept", err)
	}
}

// TestPackBuildPayloadClearsOwnershipAndSpecialBits verifies a payload carries
// neither the builder's identity nor a setuid bit into every project that installs it.
func TestPackBuildPayloadClearsOwnershipAndSpecialBits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not have these mode bits")
	}
	payloadDir := t.TempDir()
	writePayloadFile(t, payloadDir, "bin/tool", "x", 0o755)
	if err := os.Chmod(filepath.Join(payloadDir, "bin", "tool"), 0o755|os.ModeSetuid); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "payload.tar.gz")
	if err := packBuildPayload(payloadDir, "demo-1.0", buildTestPrefix, archivePath); err != nil {
		t.Fatalf("packBuildPayload() error = %v", err)
	}

	header := readPayloadEntries(t, archivePath)["demo-1.0/bin/tool"]
	if header == nil {
		t.Fatal("archive has no demo-1.0/bin/tool entry")
	}
	if header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
		t.Fatalf("header ownership = %d:%d %q:%q, want cleared", header.Uid, header.Gid, header.Uname, header.Gname)
	}
	if header.Mode != 0o755 {
		t.Fatalf("header mode = %o, want 755", header.Mode)
	}
}

// TestPackagedSymlinkTargets covers links make install commonly writes. Absolute
// links into the prefix become relative, and anything leaving the payload is
// refused, because the extractor's containment check misses absolute targets.
func TestPackagedSymlinkTargets(t *testing.T) {
	skipWithoutSymlinks(t)

	tests := []struct {
		name     string
		relative string
		target   string
		want     string
		wantErr  bool
	}{
		{name: "relative stays", relative: "bin/phar", target: "phar.phar", want: "phar.phar"},
		{name: "absolute into prefix", relative: "bin/phar", target: buildTestPrefix + "/bin/phar.phar", want: "phar.phar"},
		{name: "absolute across directories", relative: "lib/link", target: buildTestPrefix + "/bin/tool", want: "../bin/tool"},
		{name: "absolute outside prefix", relative: "bin/evil", target: "/etc/passwd", wantErr: true},
		{name: "prefix sibling", relative: "bin/evil", target: buildTestPrefix + "-other/tool", wantErr: true},
		{name: "relative escape", relative: "bin/evil", target: "../../outside", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payloadDir := t.TempDir()
			linkPath := filepath.Join(payloadDir, filepath.FromSlash(test.relative))
			if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := os.Symlink(test.target, linkPath); err != nil {
				t.Fatalf("Symlink() error = %v", err)
			}

			got, err := packagedSymlinkTarget(linkPath, test.relative, buildTestPrefix)
			if test.wantErr {
				if err == nil {
					t.Fatalf("packagedSymlinkTarget() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("packagedSymlinkTarget() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("packagedSymlinkTarget() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestPackBuildPayloadRejectsEscapingSymlink verifies an escaping link fails the
// whole package instead of being written into the cache.
func TestPackBuildPayloadRejectsEscapingSymlink(t *testing.T) {
	skipWithoutSymlinks(t)

	payloadDir := t.TempDir()
	writePayloadFile(t, payloadDir, "bin/tool", "x", 0o755)
	if err := os.Symlink("/etc/passwd", filepath.Join(payloadDir, "bin", "evil")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	err := packBuildPayload(payloadDir, "demo-1.0", buildTestPrefix, filepath.Join(t.TempDir(), "payload.tar.gz"))
	if err == nil || !strings.Contains(err.Error(), "points outside the payload") {
		t.Fatalf("packBuildPayload() error = %v, want escaping symlink rejection", err)
	}
}

// phpConfigFixture mirrors the quoting styles of real php-config and phpize output.
const phpConfigFixture = `#! /bin/sh

SED="/usr/bin/sed"
prefix="/opt/polka/build/demo"
datarootdir='/opt/polka/build/demo/php'
exec_prefix="${prefix}"
include_dir="/opt/polka/build/demo/include/php"
extension_dir='/opt/polka/build/demo/ext'
configure_options=" '--prefix=/opt/polka/build/demo' '--with-curl=shared'"
unrelated='/usr/lib'
man_dir=` + "`eval echo /opt/polka/build/demo/php/man`" + `
echo "$prefix|$extension_dir|$datarootdir|$unrelated|$man_dir|$configure_options"
`

// TestRelocateShellScriptReplacesPrefix verifies every literal prefix is gone, and
// single-quoted assignments become double-quoted so the variable can expand.
func TestRelocateShellScriptReplacesPrefix(t *testing.T) {
	relocated, err := relocateShellScript(phpConfigFixture, "bin/php-config", buildTestPrefix)
	if err != nil {
		t.Fatalf("relocateShellScript() error = %v", err)
	}
	if strings.Contains(relocated, buildTestPrefix) {
		t.Fatalf("relocated script still contains the build prefix:\n%s", relocated)
	}
	if !strings.HasPrefix(relocated, "#! /bin/sh\npolka_prefix=$(") {
		t.Fatalf("relocated script does not compute the prefix first:\n%s", relocated)
	}
	for _, want := range []string{`extension_dir="${polka_prefix}/ext"`, `datarootdir="${polka_prefix}/php"`, `unrelated='/usr/lib'`} {
		if !strings.Contains(relocated, want) {
			t.Fatalf("relocated script lacks %s:\n%s", want, relocated)
		}
	}
}

// TestRelocatedShellScriptRunsFromAnyLocation runs the rewritten script from two
// install roots, directly and through a symlink, which is the behavior PECL and PIE
// depend on.
func TestRelocatedShellScriptRunsFromAnyLocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relocated scripts are POSIX shell")
	}
	relocated, err := relocateShellScript(phpConfigFixture, "bin/php-config", buildTestPrefix)
	if err != nil {
		t.Fatalf("relocateShellScript() error = %v", err)
	}

	for _, name := range []string{"first", "second with space"} {
		root := filepath.Join(t.TempDir(), name)
		writePayloadFile(t, root, "bin/php-config", relocated, 0o755)
		scriptPath := filepath.Join(root, "bin", "php-config")
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(scriptPath, link); err != nil {
			t.Fatalf("Symlink() error = %v", err)
		}
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatalf("EvalSymlinks() error = %v", err)
		}

		for _, target := range []string{scriptPath, link} {
			output, err := exec.Command(target).CombinedOutput()
			if err != nil {
				t.Fatalf("run %s error = %v, output = %s", target, err, output)
			}
			fields := strings.Split(strings.TrimSpace(string(output)), "|")
			if fields[0] != resolvedRoot || fields[1] != resolvedRoot+"/ext" || fields[2] != resolvedRoot+"/php" || fields[3] != "/usr/lib" {
				t.Fatalf("output from %s = %s, want paths under %s", target, output, resolvedRoot)
			}
		}
	}
}

// TestRelocateShellScriptRejections verifies the rewrite refuses input it cannot
// handle safely, so a changed upstream script fails the build rather than shipping
// a broken one.
func TestRelocateShellScriptRejections(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "binary", text: "\x7fELF", want: "not a shell script"},
		{name: "no prefix", text: "#!/bin/sh\necho hi\n", want: "does not reference"},
		{name: "unsafe assignment", text: "#!/bin/sh\nx='" + buildTestPrefix + "/$y'\n", want: "cannot convert"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := relocateShellScript(test.text, "bin/x", buildTestPrefix)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("relocateShellScript() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestRelocateShellScriptDepth verifies the climb to the install root follows the
// script's depth inside the payload.
func TestRelocateShellScriptDepth(t *testing.T) {
	text := "#!/bin/sh\nx=\"" + buildTestPrefix + "\"\n"
	for relative, want := range map[string]string{"tool": `")/."`, "bin/tool": `")/.."`, "lib/php/tool": `")/../.."`} {
		relocated, err := relocateShellScript(text, relative, buildTestPrefix)
		if err != nil {
			t.Fatalf("relocateShellScript(%s) error = %v", relative, err)
		}
		if !strings.Contains(relocated, want) {
			t.Fatalf("relocated %s = %s, want climb %s", relative, relocated, want)
		}
	}
}

// TestApplyBuildPackage covers each packaging action against a real tree.
func TestApplyBuildPackage(t *testing.T) {
	sourceDir := t.TempDir()
	payloadDir := filepath.Join(t.TempDir(), "payload")
	writePayloadFile(t, sourceDir, "src/server", "binary", 0o755)
	writePayloadFile(t, payloadDir, "bin/phar", "stale", 0o755)
	writePayloadFile(t, payloadDir, "bin/php-config", "#!/bin/sh\np=\""+buildTestPrefix+"\"\n", 0o755)

	err := applyBuildPackage(manifestBuildPackage{
		Copy:            []manifestBuildCopy{{From: "src/server", To: "server"}},
		Remove:          []string{"bin/phar"},
		KeepDirs:        []string{"logs"},
		RelocateScripts: []string{"bin/php-config"},
	}, sourceDir, payloadDir, buildTestPrefix)
	if err != nil {
		t.Fatalf("applyBuildPackage() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(payloadDir, "server")); err != nil {
		t.Fatalf("Stat(server) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(payloadDir, "bin", "phar")); !os.IsNotExist(err) {
		t.Fatalf("Stat(bin/phar) error = %v, want removed", err)
	}
	if info, err := os.Stat(filepath.Join(payloadDir, "logs")); err != nil || !info.IsDir() {
		t.Fatalf("Stat(logs) info = %v, error = %v", info, err)
	}
	data, err := os.ReadFile(filepath.Join(payloadDir, "bin", "php-config"))
	if err != nil {
		t.Fatalf("ReadFile(php-config) error = %v", err)
	}
	if strings.Contains(string(data), buildTestPrefix) {
		t.Fatalf("php-config = %s, want relocated", data)
	}
	if err := applyBuildPackage(manifestBuildPackage{Copy: []manifestBuildCopy{{From: "src/missing", To: "x"}}}, sourceDir, payloadDir, buildTestPrefix); err == nil {
		t.Fatal("applyBuildPackage(missing file) error = nil, want failure")
	}
}

// TestBuildLogTail verifies a failure message gets the end of a long log.
func TestBuildLogTail(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "build.log")
	if err := os.WriteFile(logPath, []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	tail, err := buildLogTail(logPath, 2)
	if err != nil {
		t.Fatalf("buildLogTail() error = %v", err)
	}
	if tail != "three\nfour" {
		t.Fatalf("buildLogTail() = %q", tail)
	}
	if _, err := buildLogTail(filepath.Join(t.TempDir(), "missing"), 2); err == nil {
		t.Fatal("buildLogTail(missing) error = nil, want failure")
	}
}
