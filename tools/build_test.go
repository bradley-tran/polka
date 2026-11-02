package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// buildEngineManifest is a two-stage recipe exercising a private dependency, a
// payload install, packaging, and verification.
const buildEngineManifest = `id: demo
install-candidates:
  linux-amd64:
    - bin/demo
script:
  file: scripts/demo.star
  hooks: [build_sources]
build:
  platforms: [linux-amd64]
  requires:
    commands: [make]
    libraries:
      - name: libdemo
        pkg-config: libdemo
        hint: install libdemo
        skip-with: [--without-demo]
  stages:
    - name: dep
      source: dep
      prefix: deps
      configure: ["--prefix={prefix}"]
      install: [install]
    - name: demo
      source: demo
      env:
        DEMO_DIR: "{prefix}/data"
      configure: ["--prefix={prefix}", "--with-dep={deps}"]
      make: [EXTRA=1]
      install: [install]
      destdir-var: INSTALL_ROOT
  package:
    keep-dirs: [logs]
  verify:
    - [bin/demo, --version, "{install}"]
  verify-files: [bin/demo]
`

// sha256Hex returns the hex digest a source fixture publishes.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sourceTarball returns a gzip tar holding one file under a top-level directory,
// shaped like a real source release.
func sourceTarball(t *testing.T, topLevel string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	contents := []byte("#!/bin/sh\n")
	for _, header := range []*tar.Header{
		{Typeflag: tar.TypeDir, Name: topLevel + "/", Mode: 0o755},
		{Typeflag: tar.TypeReg, Name: topLevel + "/configure", Mode: 0o755, Size: int64(len(contents))},
	} {
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("WriteHeader() error = %v", err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tarWriter.Write(contents); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("Close(tar) error = %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("Close(gzip) error = %v", err)
	}

	return buffer.Bytes()
}

// buildEngineFixture wires the engine to a fake upstream, a fake compiler, and a
// host that satisfies every requirement.
type buildEngineFixture struct {
	manifest pluginManifest
	cacheDir string
	client   *http.Client
	commands *[]buildCommand
	// failOn makes the fake compiler fail when a command's arguments contain it.
	failOn *string
	// skipInstall makes the fake compiler produce no payload.
	skipInstall *bool
}

// newBuildEngineFixture installs the seams for one test and restores them after.
func newBuildEngineFixture(t *testing.T) buildEngineFixture {
	t.Helper()

	depTarball := sourceTarball(t, "dep-1.0")
	demoTarball := sourceTarball(t, "demo-2.5")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dep-1.0.tar.gz":
			_, _ = w.Write(depTarball)
		case "/demo-2.5.tar.gz":
			_, _ = w.Write(demoTarball)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	source := fmt.Sprintf(`def build_sources(ctx):
    return {
        "version": "2.5",
        "sources": {
            "dep": {"url": "%[1]s/dep-1.0.tar.gz", "filename": "dep-1.0.tar.gz", "unverified": True},
            "demo": {"url": "%[1]s/demo-2.5.tar.gz", "filename": "demo-2.5.tar.gz", "checksum_algorithm": "sha256", "checksum": "%[2]s"},
        },
    }
`, server.URL, sha256Hex(demoTarball))
	manifest := loadScriptFixture(t, buildEngineManifest, source)

	commands := []buildCommand{}
	failOn := ""
	skipInstall := false
	oldRunner, oldLookPath, oldHostCommand := buildCommandRunner, buildLookPath, buildHostCommand
	t.Cleanup(func() {
		buildCommandRunner, buildLookPath, buildHostCommand = oldRunner, oldLookPath, oldHostCommand
	})
	buildLookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }
	buildHostCommand = func(_ context.Context, name string, args ...string) (string, error) {
		return "fake " + name + "\n", nil
	}
	buildCommandRunner = func(_ context.Context, command buildCommand, log io.Writer) error {
		commands = append(commands, command)
		fmt.Fprintf(log, "ran %s\n", command.Target)
		if failOn != "" && strings.Contains(strings.Join(command.Args, " ")+" "+command.Target, failOn) {
			return errors.New("exit status 2")
		}
		// The fake install honors the destination variable like real make does.
		if !skipInstall && command.Target == "make" && slices.Contains(command.Args, "install") {
			for _, arg := range command.Args {
				if root, ok := strings.CutPrefix(arg, "INSTALL_ROOT="); ok {
					target := filepath.Join(filepath.FromSlash(root), "opt", "polka", "build", "demo", "bin", "demo")
					if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
						return err
					}
					return os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755)
				}
			}
		}
		return nil
	}

	return buildEngineFixture{
		manifest:    manifest,
		cacheDir:    t.TempDir(),
		client:      server.Client(),
		commands:    &commands,
		failOn:      &failOn,
		skipInstall: &skipInstall,
	}
}

// request returns a build request for the fixture's tool.
func (f buildEngineFixture) request() BuildRequest {
	return BuildRequest{CacheDir: f.cacheDir, Tool: "demo", Version: "2", Jobs: 3, Client: f.client, GOOS: "linux", GOARCH: "amd64"}
}

// TestBuildManifestToolRunsRecipeInOrder verifies stage order, argument rendering,
// install destinations, and environment, all of which the real compile depends on.
func TestBuildManifestToolRunsRecipeInOrder(t *testing.T) {
	fixture := newBuildEngineFixture(t)
	request := fixture.request()
	request.ConfigureArgs = []string{"--with-extra"}
	warnings := []string{}
	request.Warn = func(message string) { warnings = append(warnings, message) }

	result, err := buildManifestTool(context.Background(), fixture.manifest, request)
	if err != nil {
		t.Fatalf("buildManifestTool() error = %v", err)
	}
	if result.ResolvedVersion != "2.5" || result.RequestedVersion != "2" || !result.LocallyBuilt || result.AlreadyCached || result.Replaced {
		t.Fatalf("result = %+v", result)
	}

	deps := filepath.ToSlash(filepath.Join(fixture.cacheDir, buildDirName, "demo-2", "deps"))
	staging := filepath.ToSlash(filepath.Join(fixture.cacheDir, buildDirName, "demo-2", "staging"))
	want := []struct {
		target string
		args   []string
	}{
		{"./configure", []string{"--prefix=" + deps}},
		{"make", []string{"-j3"}},
		{"make", []string{"install"}},
		{"./configure", []string{"--prefix=/opt/polka/build/demo", "--with-dep=" + deps, "--with-extra"}},
		{"make", []string{"-j3", "EXTRA=1"}},
		{"make", []string{"install", "INSTALL_ROOT=" + staging}},
	}
	commands := *fixture.commands
	if len(commands) != len(want)+1 {
		t.Fatalf("ran %d commands, want %d plus verify: %+v", len(commands), len(want), commands)
	}
	for index, expected := range want {
		if commands[index].Target != expected.target || !slices.Equal(commands[index].Args, expected.args) {
			t.Fatalf("command %d = %s %v, want %s %v", index, commands[index].Target, commands[index].Args, expected.target, expected.args)
		}
	}

	// Stage environment reaches only that stage, with placeholders rendered.
	if !slices.Contains(commands[3].Env, "DEMO_DIR=/opt/polka/build/demo/data") {
		t.Fatalf("demo stage env lacks DEMO_DIR: %v", commands[3].Env)
	}
	if slices.Contains(commands[0].Env, "DEMO_DIR=/opt/polka/build/demo/data") {
		t.Fatal("dep stage env contains the demo stage's DEMO_DIR")
	}

	// Verification runs the extracted payload, from outside it.
	verify := commands[len(commands)-1]
	verifyDir := filepath.Join(fixture.cacheDir, buildDirName, "demo-2", "verify")
	if verify.Target != filepath.Join(verifyDir, "bin", "demo") || verify.Args[1] != filepath.ToSlash(verifyDir) {
		t.Fatalf("verify command = %+v", verify)
	}
	if verify.Dir == verifyDir {
		t.Fatal("verify ran inside the payload, which would hide working-directory bugs")
	}

	if len(warnings) != 1 || !strings.Contains(warnings[0], "dep publishes no checksum") {
		t.Fatalf("warnings = %v, want the unverified source reported", warnings)
	}
}

// TestBuildManifestToolPublishesToCache verifies the payload lands where install
// looks, keyed by the literal label, with metadata describing how it was made.
func TestBuildManifestToolPublishesToCache(t *testing.T) {
	fixture := newBuildEngineFixture(t)
	request := fixture.request()
	request.ConfigureArgs = []string{"--with-extra"}
	if _, err := buildManifestTool(context.Background(), fixture.manifest, request); err != nil {
		t.Fatalf("buildManifestTool() error = %v", err)
	}

	payload, entry, err := cachedToolPayload(fixture.cacheDir, "demo", "2")
	if err != nil {
		t.Fatalf("cachedToolPayload() error = %v", err)
	}
	if payload.DownloadedVersion != "2.5" {
		t.Fatalf("DownloadedVersion = %q, want 2.5", payload.DownloadedVersion)
	}
	if entry.Build == nil || !slices.Equal(entry.Build.ConfigureArgs, []string{"--with-extra"}) || entry.Build.Platform != "linux-amd64" {
		t.Fatalf("Build = %+v", entry.Build)
	}
	if !strings.HasPrefix(entry.SourceURL, builtPayloadSourcePrefix) {
		t.Fatalf("SourceURL = %q, want local build marker", entry.SourceURL)
	}
	if len(entry.Build.Sources) != 2 || entry.Build.Sources[0].Name != "demo" && entry.Build.Sources[1].Name != "demo" {
		t.Fatalf("Sources = %+v", entry.Build.Sources)
	}
	for _, source := range entry.Build.Sources {
		if source.Checksum == "" {
			t.Fatalf("source %s has no recorded checksum", source.Name)
		}
		if (source.Name == "dep") != source.Unverified {
			t.Fatalf("source %s Unverified = %v", source.Name, source.Unverified)
		}
	}

	// The normal install path must be able to materialize it, flattening included.
	installDir := t.TempDir()
	if _, err := InstallCachedToolPayload(fixture.cacheDir, installDir, "demo", "2"); err != nil {
		t.Fatalf("InstallCachedToolPayload() error = %v", err)
	}
	for _, relative := range []string{"bin/demo", "logs"} {
		if _, err := os.Stat(filepath.Join(installDir, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("Stat(%s) error = %v", relative, err)
		}
	}

	// A compile can reach gigabytes, so nothing but the payload and log may stay.
	if _, err := os.Stat(filepath.Join(fixture.cacheDir, buildDirName, "demo-2")); !os.IsNotExist(err) {
		t.Fatalf("Stat(build dir) error = %v, want removed", err)
	}
	if _, err := os.Stat(BuildLogPath(fixture.cacheDir, "demo", "2")); err != nil {
		t.Fatalf("Stat(log) error = %v", err)
	}
}

// TestBuildManifestToolReusesCachedLabel verifies a cached label costs no compile,
// and that changed configure arguments are an error rather than a silent no-op.
func TestBuildManifestToolReusesCachedLabel(t *testing.T) {
	fixture := newBuildEngineFixture(t)
	request := fixture.request()
	request.ConfigureArgs = []string{"--with-extra"}
	if _, err := buildManifestTool(context.Background(), fixture.manifest, request); err != nil {
		t.Fatalf("first build error = %v", err)
	}
	ran := len(*fixture.commands)

	result, err := buildManifestTool(context.Background(), fixture.manifest, request)
	if err != nil {
		t.Fatalf("second build error = %v", err)
	}
	if !result.AlreadyCached || !result.LocallyBuilt || result.ResolvedVersion != "2.5" {
		t.Fatalf("result = %+v, want cached local build", result)
	}
	if len(*fixture.commands) != ran {
		t.Fatal("second build ran commands, want none")
	}

	// No arguments means "whatever is cached is fine".
	plain := fixture.request()
	if result, err := buildManifestTool(context.Background(), fixture.manifest, plain); err != nil || !result.AlreadyCached {
		t.Fatalf("plain rebuild result = %+v, error = %v", result, err)
	}

	changed := fixture.request()
	changed.ConfigureArgs = []string{"--with-other"}
	_, err = buildManifestTool(context.Background(), fixture.manifest, changed)
	var argsChanged *BuildConfigureArgsChangedError
	if !errors.As(err, &argsChanged) {
		t.Fatalf("changed args error = %v, want BuildConfigureArgsChangedError", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error = %v, want a --force hint", err)
	}
}

// TestBuildManifestToolForceReplaces verifies --force compiles again and reports
// that existing installs still hold the old build.
func TestBuildManifestToolForceReplaces(t *testing.T) {
	fixture := newBuildEngineFixture(t)
	if _, err := buildManifestTool(context.Background(), fixture.manifest, fixture.request()); err != nil {
		t.Fatalf("first build error = %v", err)
	}
	ran := len(*fixture.commands)

	request := fixture.request()
	request.Force = true
	result, err := buildManifestTool(context.Background(), fixture.manifest, request)
	if err != nil {
		t.Fatalf("forced build error = %v", err)
	}
	if !result.Replaced || result.AlreadyCached || len(*fixture.commands) <= ran {
		t.Fatalf("result = %+v, want a replacing compile", result)
	}
}

// TestBuildManifestToolReportsFailedStage verifies a compile failure names the stage
// and carries the log's tail, keeps the log, and publishes nothing.
func TestBuildManifestToolReportsFailedStage(t *testing.T) {
	fixture := newBuildEngineFixture(t)
	*fixture.failOn = "EXTRA=1"

	_, err := buildManifestTool(context.Background(), fixture.manifest, fixture.request())
	var stageErr *BuildStageError
	if !errors.As(err, &stageErr) {
		t.Fatalf("error = %v, want BuildStageError", err)
	}
	if stageErr.Stage != "demo" || !strings.Contains(stageErr.Tail, "ran make") || !strings.Contains(err.Error(), stageErr.LogPath) {
		t.Fatalf("stage error = %+v", stageErr)
	}
	if _, err := CachedToolPayload(fixture.cacheDir, "demo", "2"); !IsCacheMiss(err) {
		t.Fatalf("CachedToolPayload() error = %v, want cache miss", err)
	}
	if _, err := os.Stat(stageErr.LogPath); err != nil {
		t.Fatalf("Stat(log) error = %v, want the log kept", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.cacheDir, buildDirName, "demo-2")); !os.IsNotExist(err) {
		t.Fatalf("Stat(build dir) error = %v, want removed after failure", err)
	}
}

// TestBuildManifestToolFailedRebuildKeepsPreviousPayload verifies a failed --force
// leaves the working build in place, since it may have taken many minutes to make.
func TestBuildManifestToolFailedRebuildKeepsPreviousPayload(t *testing.T) {
	fixture := newBuildEngineFixture(t)
	if _, err := buildManifestTool(context.Background(), fixture.manifest, fixture.request()); err != nil {
		t.Fatalf("first build error = %v", err)
	}
	*fixture.failOn = "EXTRA=1"
	request := fixture.request()
	request.Force = true
	if _, err := buildManifestTool(context.Background(), fixture.manifest, request); err == nil {
		t.Fatal("forced build error = nil, want failure")
	}
	if _, err := CachedToolPayload(fixture.cacheDir, "demo", "2"); err != nil {
		t.Fatalf("CachedToolPayload() error = %v, want previous payload kept", err)
	}
}

// TestBuildManifestToolVerification verifies a payload missing what the manifest
// promises, or failing its checks, never reaches the cache.
func TestBuildManifestToolVerification(t *testing.T) {
	t.Run("no install candidate", func(t *testing.T) {
		fixture := newBuildEngineFixture(t)
		*fixture.skipInstall = true
		_, err := buildManifestTool(context.Background(), fixture.manifest, fixture.request())
		if err == nil || !strings.Contains(err.Error(), "no install candidate") {
			t.Fatalf("error = %v, want missing candidate", err)
		}
		if _, err := CachedToolPayload(fixture.cacheDir, "demo", "2"); !IsCacheMiss(err) {
			t.Fatalf("CachedToolPayload() error = %v, want cache miss", err)
		}
	})
	t.Run("missing expected file", func(t *testing.T) {
		fixture := newBuildEngineFixture(t)
		fixture.manifest.Build.VerifyFiles = []string{"modules/mod_missing.so"}
		_, err := buildManifestTool(context.Background(), fixture.manifest, fixture.request())
		if err == nil || !strings.Contains(err.Error(), "modules/mod_missing.so") {
			t.Fatalf("error = %v, want the missing file named", err)
		}
	})
	t.Run("failing check", func(t *testing.T) {
		fixture := newBuildEngineFixture(t)
		*fixture.failOn = "--version"
		var stageErr *BuildStageError
		_, err := buildManifestTool(context.Background(), fixture.manifest, fixture.request())
		if !errors.As(err, &stageErr) || stageErr.Stage != "verify" {
			t.Fatalf("error = %v, want verify stage failure", err)
		}
	})
}

// TestBuildManifestToolRejectsBadChecksum verifies a tampered source stops the
// build before any of it is compiled.
func TestBuildManifestToolRejectsBadChecksum(t *testing.T) {
	fixture := newBuildEngineFixture(t)
	source := `def build_sources(ctx):
    return {
        "version": "2.5",
        "sources": {
            "dep": {"url": "%[1]s/dep-1.0.tar.gz", "filename": "dep-1.0.tar.gz", "unverified": True},
            "demo": {"url": "%[1]s/demo-2.5.tar.gz", "filename": "demo-2.5.tar.gz", "checksum_algorithm": "sha256", "checksum": "%[2]s"},
        },
    }
`
	// Reuse the fixture's server through the hook's URLs by loading it again.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(sourceTarball(t, "x-1"))
	}))
	t.Cleanup(server.Close)
	manifest := loadScriptFixture(t, buildEngineManifest, fmt.Sprintf(source, server.URL, phpTestChecksum("different")))

	_, err := buildManifestTool(context.Background(), manifest, BuildRequest{CacheDir: fixture.cacheDir, Tool: "demo", Version: "2", Client: server.Client(), GOOS: "linux", GOARCH: "amd64"})
	if err == nil || !strings.Contains(err.Error(), "verify demo source demo") {
		t.Fatalf("error = %v, want checksum failure", err)
	}
	if len(*fixture.commands) != 0 {
		t.Fatalf("ran %v, want nothing compiled", *fixture.commands)
	}
}

// TestBuildManifestToolChecksHostFirst verifies missing prerequisites are reported
// together before any download, and that --configure-arg can opt out of a library.
func TestBuildManifestToolChecksHostFirst(t *testing.T) {
	fixture := newBuildEngineFixture(t)
	buildLookPath = func(file string) (string, error) { return "", errors.New("not found") }
	buildHostCommand = func(context.Context, string, ...string) (string, error) { return "", errors.New("missing") }
	var requests atomic.Int32
	counting := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	t.Cleanup(counting.Close)

	request := fixture.request()
	request.Client = counting.Client()
	_, err := buildManifestTool(context.Background(), fixture.manifest, request)
	var missing *MissingHostRequirementsError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want MissingHostRequirementsError", err)
	}
	if !slices.Equal(missing.Commands, []string{"make"}) || len(missing.Libraries) != 1 || missing.Libraries[0].Name != "libdemo" {
		t.Fatalf("missing = %+v", missing)
	}
	for _, want := range []string{"make", "libdemo", "install libdemo", "--configure-arg=--without-demo"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want containing %q", err.Error(), want)
		}
	}
	if requests.Load() != 0 || len(*fixture.commands) != 0 {
		t.Fatal("work happened before the host check failed")
	}

	buildLookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }
	request = fixture.request()
	request.ConfigureArgs = []string{"--without-demo"}
	if _, err := buildManifestTool(context.Background(), fixture.manifest, request); err != nil {
		t.Fatalf("opted-out build error = %v", err)
	}
}

// TestBuildManifestToolValidatesRequest covers inputs rejected before any work.
func TestBuildManifestToolValidatesRequest(t *testing.T) {
	fixture := newBuildEngineFixture(t)
	tests := []struct {
		name   string
		mutate func(*BuildRequest)
		want   string
	}{
		{name: "bad version", mutate: func(r *BuildRequest) { r.Version = "../1" }, want: "invalid demo version"},
		{name: "negative jobs", mutate: func(r *BuildRequest) { r.Jobs = -1 }, want: "jobs must be at least 1"},
		{name: "no cache", mutate: func(r *BuildRequest) { r.CacheDir = " " }, want: "cache directory"},
		{name: "not an option", mutate: func(r *BuildRequest) { r.ConfigureArgs = []string{"rm -rf"} }, want: "invalid configure argument"},
		{name: "multiline option", mutate: func(r *BuildRequest) { r.ConfigureArgs = []string{"--x\n--y"} }, want: "invalid configure argument"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := fixture.request()
			test.mutate(&request)
			_, err := buildManifestTool(context.Background(), fixture.manifest, request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestValidateBuildConfigureArgsWithoutConfigureStage verifies a tool with no
// configure step rejects the flag instead of silently ignoring it.
func TestValidateBuildConfigureArgsWithoutConfigureStage(t *testing.T) {
	redis, err := loadBuiltinManifest(Redis)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(redis) error = %v", err)
	}
	err = validateBuildConfigureArgs(redis.Build, Redis, []string{"--x"})
	if err == nil || !strings.Contains(err.Error(), "does not accept --configure-arg") {
		t.Fatalf("error = %v, want rejection", err)
	}
	if err := validateBuildConfigureArgs(redis.Build, Redis, nil); err != nil {
		t.Fatalf("error = %v, want nil without arguments", err)
	}
}

// TestBuildManifestToolHonorsCancellation verifies Ctrl-C surfaces as cancellation,
// not as a compile failure with a misleading log tail.
func TestBuildManifestToolHonorsCancellation(t *testing.T) {
	fixture := newBuildEngineFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	buildCommandRunner = func(context.Context, buildCommand, io.Writer) error {
		cancel()
		return errors.New("signal: terminated")
	}

	_, err := buildManifestTool(ctx, fixture.manifest, fixture.request())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	var stageErr *BuildStageError
	if errors.As(err, &stageErr) {
		t.Fatalf("error = %v, want no stage failure for a cancelled build", err)
	}
}

// TestBuildToolRejectsUnbuildableTools verifies the registry entry point lists what
// can be built, on the platform asked about.
func TestBuildToolRejectsUnbuildableTools(t *testing.T) {
	registry := NewDefaultRegistry()
	ctx := context.Background()

	if _, err := BuildTool(ctx, registry, BuildRequest{Tool: "no-such-tool", Version: "1"}); err == nil || !strings.Contains(err.Error(), "unsupported tool") {
		t.Fatalf("unknown tool error = %v", err)
	}
	_, err := BuildTool(ctx, registry, BuildRequest{Tool: NodeJS, Version: "24", GOOS: "linux", GOARCH: "amd64"})
	if err == nil || !strings.Contains(err.Error(), "buildable tools: apache, nginx, php, php-zts, redis") {
		t.Fatalf("no recipe error = %v", err)
	}
	if _, err := BuildTool(ctx, registry, BuildRequest{Tool: PHP, Version: "8.4", GOOS: "windows", GOARCH: "amd64"}); err == nil || !strings.Contains(err.Error(), "cannot be built on windows/amd64") {
		t.Fatalf("wrong platform error = %v", err)
	}
}

// TestDownloadOfferingBuildMakesNoRequests verifies plain install's hint for every
// recipe tool arrives without network access, since several tools otherwise contact
// upstream first and an offline user would see the wrong error.
func TestDownloadOfferingBuildMakesNoRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	t.Cleanup(server.Close)

	for _, tool := range []string{PHP, PHPZTS, Nginx, Apache, Redis} {
		manifest, err := loadBuiltinManifest(tool)
		if err != nil {
			t.Fatalf("loadBuiltinManifest(%s) error = %v", tool, err)
		}
		err = manifestBuildRequired(manifest, "1.2", "linux", "amd64")
		var required *BuildRequiredError
		if !errors.As(err, &required) {
			t.Fatalf("%s manifestBuildRequired() error = %v, want BuildRequiredError", tool, err)
		}
		for _, want := range []string{"polka build " + tool + ":1.2", "polka install --build"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s error = %q, want containing %q", tool, err.Error(), want)
			}
		}
		if err := manifestBuildRequired(manifest, "1.2", "windows", "amd64"); err != nil {
			t.Fatalf("%s manifestBuildRequired(windows) error = %v, want downloads to proceed", tool, err)
		}
	}
	nodejs, err := loadBuiltinManifest(NodeJS)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(nodejs) error = %v", err)
	}
	if err := manifestBuildRequired(nodejs, "24", "linux", "amd64"); err != nil {
		t.Fatalf("nodejs manifestBuildRequired() error = %v, want nil without a recipe", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("made %d requests, want none", requests.Load())
	}
}

// TestSanitizedBuildEnv verifies inherited settings cannot redirect an install or
// change how make runs, and that a project's shims leave PATH.
func TestSanitizedBuildEnv(t *testing.T) {
	separator := string(os.PathListSeparator)
	env := sanitizedBuildEnv([]string{
		"HOME=/home/user",
		"CFLAGS=-O2",
		"MAKEFLAGS=-j1",
		"DESTDIR=/somewhere",
		"INSTALL_ROOT=/somewhere",
		"CONFIG_SITE=/somewhere",
		"PHPRC=/project/php.ini",
		"PHP_INI_SCAN_DIR=/project",
		"LC_ALL=de_DE.UTF-8",
		"PATH=/project/.polka/bin" + separator + "/usr/bin" + separator + "/other/.polka/bin/",
	})

	for _, want := range []string{"HOME=/home/user", "CFLAGS=-O2", "PATH=/usr/bin", "LC_ALL=C"} {
		if !slices.Contains(env, want) {
			t.Fatalf("env = %v, want %s", env, want)
		}
	}
	for _, unwanted := range []string{"MAKEFLAGS", "DESTDIR", "INSTALL_ROOT", "CONFIG_SITE", "PHPRC", "PHP_INI_SCAN_DIR"} {
		for _, entry := range env {
			if strings.HasPrefix(entry, unwanted+"=") {
				t.Fatalf("env retains %s", entry)
			}
		}
	}
	if slices.Contains(env, "LC_ALL=de_DE.UTF-8") {
		t.Fatal("env retains the inherited locale")
	}
}

// fakeDirInfo is a directory result for the libdir detection seams.
type fakeDirInfo struct {
	os.FileInfo
	dir bool
}

// IsDir reports the fake entry's type.
func (f fakeDirInfo) IsDir() bool { return f.dir }

// TestDetectBuildLibdir covers the host layouts PHP's configure cannot find alone.
// Arch's lib64 is a symlink to lib, so it must count as the default layout.
func TestDetectBuildLibdir(t *testing.T) {
	oldLookPath, oldHost, oldStat, oldLstat := buildLookPath, buildHostCommand, buildStatPath, buildLstatPath
	t.Cleanup(func() {
		buildLookPath, buildHostCommand, buildStatPath, buildLstatPath = oldLookPath, oldHost, oldStat, oldLstat
	})
	buildLookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }
	buildHostCommand = func(context.Context, string, ...string) (string, error) { return "x86_64-linux-gnu\n", nil }

	tests := []struct {
		name      string
		multiarch bool
		lib64     *fakeDirInfo
		want      string
	}{
		{name: "debian multiarch", multiarch: true, lib64: &fakeDirInfo{dir: true}, want: "lib/x86_64-linux-gnu"},
		{name: "rpm lib64", lib64: &fakeDirInfo{dir: true}, want: "lib64"},
		{name: "arch symlinked lib64", lib64: &fakeDirInfo{dir: false}, want: ""},
		{name: "plain lib", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			buildStatPath = func(string) (os.FileInfo, error) {
				if test.multiarch {
					return fakeDirInfo{dir: true}, nil
				}
				return nil, os.ErrNotExist
			}
			buildLstatPath = func(string) (os.FileInfo, error) {
				if test.lib64 != nil {
					return *test.lib64, nil
				}
				return nil, os.ErrNotExist
			}
			if got := detectBuildLibdir(context.Background()); got != test.want {
				t.Fatalf("detectBuildLibdir() = %q, want %q", got, test.want)
			}
		})
	}
}
