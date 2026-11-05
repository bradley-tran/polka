package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"polka/backend"
	"polka/tools"
)

// setBuildCommandSeams fixes the platform and replaces the compile, returning the
// requests the command made.
func setBuildCommandSeams(t *testing.T, goos string, result tools.BuildResult, err error) *[]tools.BuildRequest {
	t.Helper()
	oldGOOS, oldBuild := buildCommandGOOS, buildToolFunc
	t.Cleanup(func() { buildCommandGOOS, buildToolFunc = oldGOOS, oldBuild })

	requests := []tools.BuildRequest{}
	buildCommandGOOS = goos
	buildToolFunc = func(_ backend.Store, _ context.Context, request tools.BuildRequest) (tools.BuildResult, error) {
		requests = append(requests, request)
		return result, err
	}

	return &requests
}

// runBuildForTest runs the command in an empty directory with an isolated cache.
func runBuildForTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("POLKA_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	t.Chdir(t.TempDir())
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run(stdout, stderr, append([]string{"build"}, args...))

	return code, stdout.String(), stderr.String()
}

// TestBuildCommandRejectsNonLinux verifies other platforms get an explanation
// rather than an unknown-command error, before anything else is examined.
func TestBuildCommandRejectsNonLinux(t *testing.T) {
	requests := setBuildCommandSeams(t, "windows", tools.BuildResult{}, nil)

	code, _, stderr := runBuildForTest(t, "php:8.4")
	if code != 1 || !strings.Contains(stderr, "only supported on Linux") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if len(*requests) != 0 {
		t.Fatalf("requests = %+v, want no build", *requests)
	}
}

// TestBuildCommandValidatesArguments covers input rejected before any build runs.
func TestBuildCommandValidatesArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no argument", want: "requires one tool:version"},
		{name: "two arguments", args: []string{"php:8.4", "nginx:1.30"}, want: "requires one tool:version"},
		{name: "no version", args: []string{"php"}, want: "must be TOOL:VERSION"},
		{name: "empty version", args: []string{"php:"}, want: "must be TOOL:VERSION"},
		{name: "extra colon", args: []string{"php:8:4"}, want: "must be TOOL:VERSION"},
		{name: "zero jobs", args: []string{"php:8.4", "--jobs", "0"}, want: "--jobs must be at least 1"},
		{name: "unknown flag", args: []string{"php:8.4", "--nope"}, want: "unknown flag"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := setBuildCommandSeams(t, "linux", tools.BuildResult{}, nil)
			code, _, stderr := runBuildForTest(t, test.args...)
			if code != 1 || !strings.Contains(stderr, test.want) {
				t.Fatalf("code = %d, stderr = %q, want containing %q", code, stderr, test.want)
			}
			if len(*requests) != 0 {
				t.Fatalf("requests = %+v, want no build", *requests)
			}
		})
	}
}

// TestBuildCommandPassesOptions verifies flags reach the build, and that the tool
// ID is normalized, from a directory with no Polka project.
func TestBuildCommandPassesOptions(t *testing.T) {
	requests := setBuildCommandSeams(t, "linux", tools.BuildResult{Tool: "php", RequestedVersion: "8.4", ResolvedVersion: "8.4.27", PayloadPath: "/cache/php.tar.gz"}, nil)

	code, stdout, stderr := runBuildForTest(t, "PHP:8.4", "--configure-arg=--without-tidy", "--configure-arg=--with-x", "--jobs", "4", "--force")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	request := (*requests)[0]
	if request.Tool != "php" || request.Version != "8.4" || request.Jobs != 4 || !request.Force {
		t.Fatalf("request = %+v", request)
	}
	if !slices.Equal(request.ConfigureArgs, []string{"--without-tidy", "--with-x"}) {
		t.Fatalf("ConfigureArgs = %v", request.ConfigureArgs)
	}
	for _, want := range []string{"Built php 8.4 (8.4.27)", "/cache/php.tar.gz", "tools.php to 8.4"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want containing %q", stdout, want)
		}
	}
}

// TestBuildCommandReportsOutcomes verifies the message for each result tells the
// user what actually happened and what to do next.
func TestBuildCommandReportsOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		result  tools.BuildResult
		want    string
		notWant string
	}{
		{
			name:    "cached local build",
			result:  tools.BuildResult{AlreadyCached: true, LocallyBuilt: true, ResolvedVersion: "8.4.27"},
			want:    "already cached (8.4.27, locally built); pass --force",
			notWant: "Built",
		},
		{
			name:   "cached download",
			result: tools.BuildResult{AlreadyCached: true, ResolvedVersion: "8.4.27"},
			want:   "already cached (8.4.27, downloaded)",
		},
		{
			name:   "replacement",
			result: tools.BuildResult{Replaced: true, ResolvedVersion: "8.4.27"},
			want:   `keep the previous build; run "polka install --force"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setBuildCommandSeams(t, "linux", test.result, nil)
			code, stdout, stderr := runBuildForTest(t, "php:8.4")
			if code != 0 {
				t.Fatalf("code = %d, stderr = %q", code, stderr)
			}
			if !strings.Contains(stdout, test.want) {
				t.Fatalf("stdout = %q, want containing %q", stdout, test.want)
			}
			if test.notWant != "" && strings.Contains(stdout, test.notWant) {
				t.Fatalf("stdout = %q, want no %q", stdout, test.notWant)
			}
		})
	}
}

// TestBuildCommandReportsFailures verifies errors keep their detail, and that a
// cancelled build uses the conventional interrupted exit code.
func TestBuildCommandReportsFailures(t *testing.T) {
	setBuildCommandSeams(t, "linux", tools.BuildResult{}, errors.New("cannot build php: missing host requirements"))
	if code, _, stderr := runBuildForTest(t, "php:8.4"); code != 1 || !strings.Contains(stderr, "missing host requirements") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}

	setBuildCommandSeams(t, "linux", tools.BuildResult{}, context.Canceled)
	if code, _, stderr := runBuildForTest(t, "php:8.4"); code != 130 || !strings.Contains(stderr, "cancelled") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}

// TestBuildCommandHelpAndRootUsage verifies the command is discoverable.
func TestBuildCommandHelpAndRootUsage(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := Run(stdout, stderr, []string{"build", "--help"}); code != 0 {
		t.Fatalf("Run(build --help) code = %d", code)
	}
	if !strings.Contains(stdout.String(), "--configure-arg") {
		t.Fatalf("build help = %q", stdout.String())
	}
	stdout.Reset()
	if code := Run(stdout, stderr, []string{"--help"}); code != 0 {
		t.Fatalf("Run(--help) code = %d", code)
	}
	if !strings.Contains(stdout.String(), "build <tool:version>") {
		t.Fatalf("root help = %q", stdout.String())
	}
}

// TestParseBuildToolVersion covers argument normalization.
func TestParseBuildToolVersion(t *testing.T) {
	tool, version, err := parseBuildToolVersion(" PHP-ZTS : 8.4 ")
	if err != nil || tool != "php-zts" || version != "8.4" {
		t.Fatalf("parseBuildToolVersion() = %q, %q, %v", tool, version, err)
	}
}

// TestInstallBuildFlagUsesCachedPayload verifies --build is accepted through the
// top-level shortcut, says where logs go, and needs no compile for a cached tool.
func TestInstallBuildFlagUsesCachedPayload(t *testing.T) {
	projectDir := t.TempDir()
	cacheDir := filepath.Join(projectDir, "global-cache")
	t.Setenv("POLKA_CACHE_DIR", cacheDir)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}

	writeCachedPHP(t, cacheDir, "8.4", fakePHPScript())
	runTestConfigValue(t, stdout, stderr, projectDir, "demo", "tools.php", "8.4")

	stdout.Reset()
	stderr.Reset()
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "install", "--env", "demo", "--build"}); code != 0 {
		t.Fatalf("Run(install --build) code = %d, stderr = %q", code, stderr.String())
	}
	if want := "Compile logs are written to " + filepath.Join(cacheDir, ".build-logs"); !strings.Contains(stdout.String(), want) {
		t.Fatalf("stdout = %q, want containing %q", stdout.String(), want)
	}
	if !strings.Contains(stdout.String(), "php 8.4\t(cached)") {
		t.Fatalf("stdout = %q, want php reported as cached", stdout.String())
	}

	stdout.Reset()
	if code := Run(stdout, stderr, []string{"--dir", projectDir, "install", "--env", "demo"}); code != 0 {
		t.Fatalf("Run(install) code = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "Compile logs") {
		t.Fatalf("stdout = %q, want no compile message without --build", stdout.String())
	}
}
