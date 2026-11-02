package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"polka/tools"
)

// fakeToolBuilder records build requests and stands in for a compile by writing
// the same cached payload a downloaded tool would have.
type fakeToolBuilder struct {
	t        *testing.T
	cacheDir string
	requests *[]tools.BuildRequest
	err      error
}

// Build records the request and publishes a fixture payload.
func (b fakeToolBuilder) Build(_ context.Context, request tools.BuildRequest) (tools.BuildResult, error) {
	*b.requests = append(*b.requests, request)
	if b.err != nil {
		return tools.BuildResult{}, b.err
	}
	writeCachedTool(b.t, b.cacheDir, request.Tool, request.Version)

	return tools.BuildResult{Tool: request.Tool, RequestedVersion: request.Version, ResolvedVersion: request.Version}, nil
}

// newBuildTestStore returns a store with a demo environment configuring php 8.4
// and a fake builder. Recipes only exist for Linux, so the pass under test has
// nothing to do elsewhere.
func newBuildTestStore(t *testing.T) (Store, *[]tools.BuildRequest) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("build recipes only exist on Linux")
	}

	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_TOOLS_DIR", filepath.Join(projectDir, "internal-tools"))
	requests := []tools.BuildRequest{}
	store.Builder = fakeToolBuilder{t: t, cacheDir: store.CacheDir, requests: &requests}
	if _, err := store.Configure("demo", "8.4", "", "", nil); err != nil {
		t.Fatalf("Configure(demo) error = %v", err)
	}

	return store, &requests
}

// TestInstallWithoutBuildReportsBuildRequired verifies plain install explains the
// next step rather than compiling or attempting a download that cannot exist.
func TestInstallWithoutBuildReportsBuildRequired(t *testing.T) {
	store, requests := newBuildTestStore(t)

	_, err := store.InstallWithProgress("demo", InstallOptions{}, nil)
	var required *tools.BuildRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("InstallWithProgress() error = %v, want BuildRequiredError", err)
	}
	if required.Tool != toolPHP || required.Version != "8.4" {
		t.Fatalf("required = %+v", required)
	}
	if len(*requests) != 0 {
		t.Fatalf("builder called %v, want no compile without --build", *requests)
	}
}

// TestInstallWithBuildCompilesMissingTool verifies --build compiles the missing
// label, installs it through the normal path, and reports it as built.
func TestInstallWithBuildCompilesMissingTool(t *testing.T) {
	store, requests := newBuildTestStore(t)
	warn := func(string) {}
	stages := []InstallProgressStage{}

	results, err := store.InstallWithProgress("demo", InstallOptions{Build: true, Warn: warn}, func(progress InstallProgress) {
		stages = append(stages, progress.Stage)
	})
	if err != nil {
		t.Fatalf("InstallWithProgress() error = %v", err)
	}
	if len(*requests) != 1 || (*requests)[0].Tool != toolPHP || (*requests)[0].Version != "8.4" || (*requests)[0].CacheDir != store.CacheDir {
		t.Fatalf("builder requests = %+v", *requests)
	}
	if len(results) != 1 || !results[0].Built || results[0].TargetPath == "" {
		t.Fatalf("results = %+v, want one built install", results)
	}
	if !slices.Contains(stages, InstallProgressBuilding) || stages[0] != InstallProgressBuilding {
		t.Fatalf("stages = %v, want building first", stages)
	}
	if _, err := os.Stat(results[0].TargetPath); err != nil {
		t.Fatalf("Stat(target) error = %v", err)
	}
}

// TestInstallBuildContextOnlyCoversCompiles verifies the interrupt-watching context
// is only requested for an actual compile and is released afterwards. A caller's
// interrupt handler must not outlive the compile, or Ctrl-C would stop working for
// the rest of the install and for installs that compile nothing.
func TestInstallBuildContextOnlyCoversCompiles(t *testing.T) {
	store, _ := newBuildTestStore(t)
	acquired, released := 0, 0
	options := InstallOptions{Build: true, BuildContext: func() (context.Context, context.CancelFunc) {
		acquired++
		ctx, cancel := context.WithCancel(context.Background())
		return ctx, func() {
			released++
			cancel()
		}
	}}

	if _, err := store.InstallWithProgress("demo", options, nil); err != nil {
		t.Fatalf("first install error = %v", err)
	}
	if acquired != 1 || released != 1 {
		t.Fatalf("after compile: acquired = %d, released = %d, want 1 and 1", acquired, released)
	}
	if _, err := store.InstallWithProgress("demo", options, nil); err != nil {
		t.Fatalf("second install error = %v", err)
	}
	if acquired != 1 {
		t.Fatalf("after no-op install: acquired = %d, want no new context", acquired)
	}
}

// TestInstallWithBuildSkipsWhatExists verifies --build only compiles what is
// missing: a second install and a cached label both cost no compile.
func TestInstallWithBuildSkipsWhatExists(t *testing.T) {
	store, requests := newBuildTestStore(t)

	if _, err := store.InstallWithProgress("demo", InstallOptions{Build: true}, nil); err != nil {
		t.Fatalf("first install error = %v", err)
	}
	if _, err := store.InstallWithProgress("demo", InstallOptions{Build: true}, nil); err != nil {
		t.Fatalf("second install error = %v", err)
	}
	// --force reinstalls from the cache. It must not throw away a long compile.
	results, err := store.InstallWithProgress("demo", InstallOptions{Build: true, Force: true}, nil)
	if err != nil {
		t.Fatalf("forced install error = %v", err)
	}
	if len(*requests) != 1 {
		t.Fatalf("builder called %d times, want 1", len(*requests))
	}
	if results[0].Built || results[0].Skipped {
		t.Fatalf("forced install result = %+v, want a reinstall from cache", results[0])
	}
}

// TestInstallWithBuildPropagatesFailure verifies a failed compile stops the install
// and names the tool, with nothing recorded as installed.
func TestInstallWithBuildPropagatesFailure(t *testing.T) {
	store, requests := newBuildTestStore(t)
	store.Builder = fakeToolBuilder{t: t, cacheDir: store.CacheDir, requests: requests, err: errors.New("compiler exploded")}

	_, err := store.InstallWithProgress("demo", InstallOptions{Build: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "build php 8.4: compiler exploded") {
		t.Fatalf("InstallWithProgress() error = %v, want the failure named", err)
	}
	if store.readInstallState().has(toolPHP, "8.4") {
		t.Fatal("failed build was recorded as installed")
	}
}

// TestInstallToolWithOptionsBuilds verifies the explicit tool:version path compiles
// too, and only records the version in config after the install succeeds.
func TestInstallToolWithOptionsBuilds(t *testing.T) {
	store, requests := newBuildTestStore(t)

	result, err := store.InstallToolWithOptions("demo", toolPHP, "8.3", InstallOptions{Build: true}, nil)
	if err != nil {
		t.Fatalf("InstallToolWithOptions() error = %v", err)
	}
	if !result.Built || len(*requests) != 1 || (*requests)[0].Version != "8.3" {
		t.Fatalf("result = %+v, requests = %+v", result, *requests)
	}
	config, err := store.loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if got := config.Environments["demo"].ToolVersion(toolPHP); got != "8.3" {
		t.Fatalf("configured php = %q, want 8.3", got)
	}

	if _, err := store.InstallTool("demo", toolPHP, "8.2"); err == nil || !strings.Contains(err.Error(), "polka build php:8.2") {
		t.Fatalf("InstallTool() error = %v, want a build hint without --build", err)
	}
	config, err = store.loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if got := config.Environments["demo"].ToolVersion(toolPHP); got != "8.3" {
		t.Fatalf("configured php = %q after failed install, want 8.3 kept", got)
	}
}

// TestInstallWithBuildLeavesDownloadableToolsAlone verifies a tool with no recipe
// goes through the downloader as usual.
func TestInstallWithBuildLeavesDownloadableToolsAlone(t *testing.T) {
	store, requests := newBuildTestStore(t)
	writeCachedTool(t, store.CacheDir, toolPHP, "8.4")
	writeCachedTool(t, store.CacheDir, toolNodeJS, "24")

	if _, err := store.InstallToolWithOptions("demo", toolNodeJS, "24", InstallOptions{Build: true}, nil); err != nil {
		t.Fatalf("InstallToolWithOptions() error = %v", err)
	}
	if len(*requests) != 0 {
		t.Fatalf("builder requests = %+v, want none for a tool without a recipe", *requests)
	}
}

// TestInternalBuildRequest verifies internal PHP is only compiled for environments
// with PIE extensions, because internal tools never compile on demand and PIE
// would otherwise fail partway through the install.
func TestInternalBuildRequest(t *testing.T) {
	store, _ := newBuildTestStore(t)

	if _, needed, err := store.InternalBuildRequest(Environment{}); err != nil || needed {
		t.Fatalf("InternalBuildRequest(no PIE) needed = %v, error = %v", needed, err)
	}
	environment := Environment{PIEExtensions: map[string]string{"xdebug/xdebug": "*"}}
	request, needed, err := store.InternalBuildRequest(environment)
	if err != nil || !needed {
		t.Fatalf("InternalBuildRequest(PIE) needed = %v, error = %v", needed, err)
	}
	if request.Tool != toolPHP || request.Version != DefaultPHPVersion {
		t.Fatalf("request = %+v", request)
	}

	// An existing internal install needs nothing compiled.
	internal := store.internalStore()
	state := internal.readInstallState()
	state.record(toolPHP, DefaultPHPVersion)
	if err := internal.writeInstallState(state); err != nil {
		t.Fatalf("writeInstallState() error = %v", err)
	}
	writeCachedTool(t, store.CacheDir, toolPHP, DefaultPHPVersion)
	if _, err := internal.installToolFromCache(toolPHP, DefaultPHPVersion); err != nil {
		t.Fatalf("installToolFromCache() error = %v", err)
	}
	if _, needed, err := store.InternalBuildRequest(environment); err != nil || needed {
		t.Fatalf("InternalBuildRequest(installed) needed = %v, error = %v", needed, err)
	}
}

// replacingToolBuilder reports a rebuild that replaced an existing payload.
type replacingToolBuilder struct{}

// Build reports a replacement without doing any work.
func (replacingToolBuilder) Build(_ context.Context, request tools.BuildRequest) (tools.BuildResult, error) {
	return tools.BuildResult{Tool: request.Tool, RequestedVersion: request.Version, Replaced: true}, nil
}

// TestBuildToolForgetsInternalInstallOnReplace verifies a rebuild reaches internal
// tools, which have no --force of their own, without touching other versions.
func TestBuildToolForgetsInternalInstallOnReplace(t *testing.T) {
	store, _ := newBuildTestStore(t)
	internal := store.internalStore()
	state := internal.readInstallState()
	state.record(toolPHP, "8.4")
	state.record(toolPHP, "8.3")
	if err := internal.writeInstallState(state); err != nil {
		t.Fatalf("writeInstallState() error = %v", err)
	}

	store.Builder = replacingToolBuilder{}
	if _, err := store.BuildTool(context.Background(), tools.BuildRequest{Tool: toolPHP, Version: "8.4"}); err != nil {
		t.Fatalf("BuildTool() error = %v", err)
	}
	state = internal.readInstallState()
	if state.has(toolPHP, "8.4") || !state.has(toolPHP, "8.3") {
		t.Fatalf("state = %+v, want only 8.4 forgotten", state)
	}
}

// TestInstallStateForget covers removing the last version of a tool.
func TestInstallStateForget(t *testing.T) {
	state := installState{}
	state.record(toolPHP, "8.4")
	state.forget(toolPHP, "8.3")
	if !state.has(toolPHP, "8.4") {
		t.Fatal("forgetting another version removed 8.4")
	}
	state.forget(toolPHP, "8.4")
	if _, ok := state.Tools[toolPHP]; ok {
		t.Fatalf("Tools = %v, want the empty tool entry removed", state.Tools)
	}
}
