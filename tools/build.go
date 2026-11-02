package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

const (
	// buildDirName, buildLogDirName, and buildLockDirName sit beside the per-tool
	// cache directories. Their leading dot keeps them from colliding with a tool ID.
	buildDirName     = ".build"
	buildLogDirName  = ".build-logs"
	buildLockDirName = ".build-locks"
	// buildPrefixRoot is the path compiled into built tools. Staged installs go
	// through a destination directory, so nothing is ever written here. The path
	// must not exist at runtime, which keeps relocation mistakes visible rather
	// than silently loading another copy of the tool.
	buildPrefixRoot = "/opt/polka/build"
	// buildLogTailLines is how much of the log a failure message includes.
	buildLogTailLines = 40
	// builtPayloadSourcePrefix marks a local build in cache metadata, where a
	// download records its URL.
	builtPayloadSourcePrefix = "local-build:"
)

// BuildRequest describes one `polka build` run.
type BuildRequest struct {
	CacheDir      string
	Tool          string
	Version       string
	ConfigureArgs []string
	// Jobs is the make parallelism; zero uses every CPU.
	Jobs int
	// Force rebuilds a label the cache already holds.
	Force bool
	// Client overrides the HTTP client used for source downloads.
	Client *http.Client
	// Status receives short progress messages.
	Status func(message string)
	// Warn receives problems worth keeping after the build, such as a source
	// with no published checksum.
	Warn func(message string)
	// GOOS and GOARCH override the target platform in tests; empty uses the host.
	GOOS   string
	GOARCH string
}

// BuildResult describes the payload a build produced or found.
type BuildResult struct {
	Tool             string
	RequestedVersion string
	ResolvedVersion  string
	PayloadPath      string
	// AlreadyCached reports that no compile ran.
	AlreadyCached bool
	// LocallyBuilt reports whether the cached payload came from `polka build`.
	LocallyBuilt bool
	// Replaced reports that a force rebuild overwrote an earlier payload, so
	// existing project installs still hold the previous build.
	Replaced bool
	LogPath  string
}

// BuildablePlugin is implemented by plugins with a compile recipe. It is
// optional so plugins without one, including test fakes, need no changes.
type BuildablePlugin interface {
	Buildable(goos, goarch string) bool
	Build(ctx context.Context, request BuildRequest) (BuildResult, error)
}

// BuildRequiredError reports a tool with no download for this platform that
// `polka build` can provide. Install surfaces it as the next step to take.
type BuildRequiredError struct {
	Tool    string
	Version string
}

// Error names both ways to produce the missing payload.
func (e *BuildRequiredError) Error() string {
	return fmt.Sprintf(`%s %s has no prebuilt Linux download; run "polka build %s:%s" or "polka install --build"`, e.Tool, e.Version, e.Tool, e.Version)
}

// BuildConfigureArgsChangedError reports that a cached build used different
// configure arguments than requested. Silently keeping it would ignore the
// request, and silently replacing it would discard a long compile.
type BuildConfigureArgsChangedError struct {
	Tool    string
	Version string
}

// Error tells the user how to replace the cached build.
func (e *BuildConfigureArgsChangedError) Error() string {
	return fmt.Sprintf("%s %s is already cached with different configure arguments; add --force to rebuild it", e.Tool, e.Version)
}

// BuildStageError reports a failed compile step with the end of its log.
type BuildStageError struct {
	Tool    string
	Stage   string
	LogPath string
	Tail    string
	Err     error
}

// Error includes the log tail, since that is where a compile's reason is.
func (e *BuildStageError) Error() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "build %s failed at stage %s: %v", e.Tool, e.Stage, e.Err)
	if e.Tail != "" {
		fmt.Fprintf(&builder, "\n--- last lines of %s ---\n%s", e.LogPath, e.Tail)
	} else {
		fmt.Fprintf(&builder, "\nsee %s", e.LogPath)
	}

	return builder.String()
}

// Unwrap exposes the underlying failure, such as context cancellation.
func (e *BuildStageError) Unwrap() error {
	return e.Err
}

// buildCommand is one process in a build.
type buildCommand struct {
	Dir    string
	Target string
	Args   []string
	Env    []string
}

// buildCommandRunner runs compile commands. Tests replace it to exercise the
// whole engine without a compiler, including on Windows CI.
var buildCommandRunner = runBuildCommand

// runBuildCommand runs a command with its output going to the build log only.
func runBuildCommand(ctx context.Context, command buildCommand, log io.Writer) error {
	process := exec.CommandContext(ctx, command.Target, command.Args...)
	process.Dir = command.Dir
	process.Env = command.Env
	process.Stdout = log
	process.Stderr = log
	configureBuildProcess(process)

	return process.Run()
}

// BuildTool compiles a tool through its plugin's recipe.
func BuildTool(ctx context.Context, registry *Registry, request BuildRequest) (BuildResult, error) {
	if registry == nil {
		registry = NewDefaultRegistry()
	}
	plugin, ok := registry.Plugin(request.Tool)
	if !ok {
		return BuildResult{}, fmt.Errorf("unsupported tool %q", request.Tool)
	}
	buildable, ok := plugin.(BuildablePlugin)
	goos, goarch := request.platform()
	if !ok || !buildable.Buildable(goos, goarch) {
		return BuildResult{}, fmt.Errorf("%s cannot be built on %s/%s; buildable tools: %s", request.Tool, goos, goarch, strings.Join(registry.BuildableTools(goos, goarch), ", "))
	}

	return buildable.Build(ctx, request)
}

// Buildable reports whether a registered tool has a recipe for the platform.
func (r *Registry) Buildable(tool, goos, goarch string) bool {
	plugin, ok := r.Plugin(tool)
	if !ok {
		return false
	}
	buildable, ok := plugin.(BuildablePlugin)

	return ok && buildable.Buildable(goos, goarch)
}

// BuildableTools lists the tools with a recipe for the platform, sorted.
func (r *Registry) BuildableTools(goos, goarch string) []string {
	tools := []string{}
	for _, plugin := range r.plugins {
		if buildable, ok := plugin.(BuildablePlugin); ok && buildable.Buildable(goos, goarch) {
			tools = append(tools, plugin.ID())
		}
	}
	slices.Sort(tools)

	return tools
}

// platform returns the request's target, defaulting to the host.
func (r BuildRequest) platform() (string, string) {
	goos, goarch := r.GOOS, r.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}

	return goos, goarch
}

// status forwards a progress message when the caller wants them.
func (r BuildRequest) status(format string, args ...any) {
	if r.Status != nil {
		r.Status(fmt.Sprintf(format, args...))
	}
}

// warn forwards a problem worth keeping when the caller wants them.
func (r BuildRequest) warn(format string, args ...any) {
	if r.Warn != nil {
		r.Warn(fmt.Sprintf(format, args...))
	}
}

// BuildLogDir returns the directory holding compile logs for a cache.
func BuildLogDir(cacheDir string) string {
	return filepath.Join(cacheDir, buildLogDirName)
}

// BuildLogPath returns where a build of tool at label writes its log.
func BuildLogPath(cacheDir, tool, label string) string {
	return filepath.Join(BuildLogDir(cacheDir), tool+"-"+label+".log")
}

// buildManifestTool runs the recipe: check the cache and host, take the lock,
// fetch and verify sources, compile, package, verify the result as installed,
// and publish it to the cache.
func buildManifestTool(ctx context.Context, m pluginManifest, request BuildRequest) (BuildResult, error) {
	tool := strings.ToLower(strings.TrimSpace(m.ID))
	label := strings.TrimSpace(request.Version)
	if err := validateVersion(tool, label); err != nil {
		return BuildResult{}, err
	}
	if strings.TrimSpace(request.CacheDir) == "" {
		return BuildResult{}, fmt.Errorf("build %s: cache directory cannot be empty", tool)
	}
	jobs := request.Jobs
	if jobs < 0 {
		return BuildResult{}, fmt.Errorf("build %s: jobs must be at least 1", tool)
	}
	if jobs == 0 {
		jobs = runtime.NumCPU()
	}
	if err := validateBuildConfigureArgs(m.Build, tool, request.ConfigureArgs); err != nil {
		return BuildResult{}, err
	}

	result := BuildResult{Tool: tool, RequestedVersion: label, LogPath: BuildLogPath(request.CacheDir, tool, label)}
	if !request.Force {
		found, cached, err := existingBuild(request.CacheDir, tool, label, request.ConfigureArgs, result)
		if err != nil || found {
			return cached, err
		}
	}
	if err := checkBuildHostRequirements(ctx, tool, m.Build.Requires, request.ConfigureArgs); err != nil {
		return BuildResult{}, err
	}

	lockPath := filepath.Join(request.CacheDir, buildLockDirName, tool+"-"+label+".lock")
	release, err := acquireBuildLock(ctx, lockPath, func() {
		request.status("waiting for another build of %s %s", tool, label)
	})
	if err != nil {
		return BuildResult{}, err
	}
	defer func() {
		if releaseErr := release(); releaseErr != nil {
			request.status("release build lock: %v", releaseErr)
		}
	}()
	// A build that held the lock may have just produced this payload.
	if !request.Force {
		found, cached, err := existingBuild(request.CacheDir, tool, label, request.ConfigureArgs, result)
		if err != nil || found {
			return cached, err
		}
	}
	_, previousErr := CachedToolPayload(request.CacheDir, tool, label)
	result.Replaced = previousErr == nil

	return runBuild(ctx, m, request, result, jobs)
}

// existingBuild reports a usable cached payload for the label. A cached build
// with different configure arguments is an error, not a reuse.
func existingBuild(cacheDir, tool, label string, configureArgs []string, result BuildResult) (bool, BuildResult, error) {
	payload, entry, err := cachedToolPayload(cacheDir, tool, label)
	if err != nil {
		if IsCacheMiss(err) {
			return false, BuildResult{}, nil
		}
		return false, BuildResult{}, err
	}

	var cachedArgs []string
	if entry.Build != nil {
		cachedArgs = entry.Build.ConfigureArgs
	}
	if len(configureArgs) > 0 && !slices.Equal(cachedArgs, configureArgs) {
		return false, BuildResult{}, &BuildConfigureArgsChangedError{Tool: tool, Version: label}
	}

	result.ResolvedVersion = payload.DownloadedVersion
	result.PayloadPath = payload.PayloadPath
	result.AlreadyCached = true
	result.LocallyBuilt = entry.Build != nil

	return true, result, nil
}

// validateBuildConfigureArgs rejects arguments that cannot be configure options,
// or a recipe that has no configure step to pass them to.
func validateBuildConfigureArgs(build manifestBuild, tool string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if build.configureStageIndex() < 0 {
		return fmt.Errorf("%s has no configure step, so it does not accept --configure-arg", tool)
	}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") || strings.ContainsAny(arg, "\r\n\x00") {
			return fmt.Errorf("invalid configure argument %q: use a single --option", arg)
		}
	}

	return nil
}

// runBuild performs the work once the lock is held. The build directory is
// reset first, so a crashed earlier run leaves nothing behind that matters, and
// removed afterwards on any outcome, because it can reach gigabytes. The log stays.
func runBuild(ctx context.Context, m pluginManifest, request BuildRequest, result BuildResult, jobs int) (BuildResult, error) {
	tool := result.Tool
	label := result.RequestedVersion
	goos, goarch := request.platform()

	client := request.Client
	if client == nil {
		client = newDownloadClient()
	}
	request.status("resolving %s %s sources", tool, label)
	plan, err := resolveBuildSources(client, m, label, goos, goarch)
	if err != nil {
		return BuildResult{}, err
	}
	result.ResolvedVersion = plan.Version

	buildRoot := filepath.Join(request.CacheDir, buildDirName, tool+"-"+label)
	if err := os.RemoveAll(buildRoot); err != nil {
		return BuildResult{}, fmt.Errorf("reset build directory: %w", err)
	}
	if err := os.MkdirAll(buildRoot, 0o755); err != nil {
		return BuildResult{}, fmt.Errorf("create build directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(buildRoot); err != nil {
			request.status("remove build directory %s: %v", buildRoot, err)
		}
	}()

	if err := os.MkdirAll(filepath.Dir(result.LogPath), 0o755); err != nil {
		return BuildResult{}, fmt.Errorf("create build log directory: %w", err)
	}
	logFile, err := os.Create(result.LogPath)
	if err != nil {
		return BuildResult{}, fmt.Errorf("create build log: %w", err)
	}
	logClosed := false
	closeLog := func() error {
		if logClosed {
			return nil
		}
		logClosed = true
		return logFile.Close()
	}
	defer func() {
		if err := closeLog(); err != nil {
			request.status("close build log: %v", err)
		}
	}()
	request.status("building %s %s; log: %s", tool, plan.Version, result.LogPath)

	sources, err := fetchBuildSources(ctx, client, request, tool, plan, buildRoot)
	if err != nil {
		return BuildResult{}, err
	}

	run := buildRun{
		tool:     tool,
		label:    label,
		manifest: m,
		request:  request,
		jobs:     jobs,
		log:      logFile,
		logPath:  result.LogPath,
		root:     buildRoot,
		prefix:   filepath.ToSlash(filepath.Join(buildPrefixRoot, tool)),
		env:      sanitizedBuildEnv(os.Environ()),
	}
	sourceDirs, err := run.extractSources(sources)
	if err != nil {
		return BuildResult{}, err
	}
	finalSourceDir := ""
	for index, stage := range m.Build.Stages {
		sourceDir := sourceDirs[strings.TrimSpace(stage.Source)]
		finalSourceDir = sourceDir
		if err := run.runStage(ctx, index, stage, sourceDir); err != nil {
			return BuildResult{}, err
		}
	}

	archivePath, err := run.packageAndVerify(ctx, finalSourceDir, plan.Version, goos, goarch)
	if err != nil {
		return BuildResult{}, err
	}

	// Close before publishing so a close failure cannot follow a success message.
	if err := closeLog(); err != nil {
		return BuildResult{}, fmt.Errorf("close build log: %w", err)
	}
	payloadPath, err := CacheBuiltPayload(request.CacheDir, tool, label, BuiltPayload{
		ResolvedVersion: plan.Version,
		FileName:        filepath.Base(archivePath),
		ArchivePath:     archivePath,
		Info: BuildInfo{
			Platform:      platformKey(goos, goarch),
			Compiler:      describeBuildHostCompiler(ctx),
			ConfigureArgs: append([]string(nil), request.ConfigureArgs...),
			Sources:       sources,
			BuiltAt:       time.Now().UTC(),
		},
	})
	if err != nil {
		return BuildResult{}, err
	}
	result.PayloadPath = payloadPath
	result.LocallyBuilt = true

	return result, nil
}

// fetchBuildSources downloads and verifies every source in the plan, returning
// what was fetched for the cache metadata. A source's unverified flag is the only
// way past verification, and it is reported to the user.
func fetchBuildSources(ctx context.Context, client *http.Client, request BuildRequest, tool string, plan buildSourcePlan, buildRoot string) ([]BuildSourceInfo, error) {
	downloadDir := filepath.Join(buildRoot, "downloads")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return nil, fmt.Errorf("create source download directory: %w", err)
	}

	infos := []BuildSourceInfo{}
	for _, source := range sortedBuildSources(plan) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		request.status("downloading %s", source.FileName)
		archivePath := filepath.Join(downloadDir, source.FileName)
		if err := downloadFile(client, source.URL, archivePath); err != nil {
			return nil, fmt.Errorf("download %s source %s: %w", tool, source.Name, err)
		}

		algorithm := source.ChecksumAlgorithm
		checksum := source.Checksum
		if source.Unverified {
			request.warn("%s publishes no checksum; %s was verified by TLS only", source.Name, source.FileName)
			algorithm = checksumAlgorithmSHA256
			computed, _, err := checksumFile(algorithm, archivePath)
			if err != nil {
				return nil, err
			}
			checksum = computed
		} else {
			if checksum == "" {
				published, err := downloadChecksumValue(client, source.ChecksumURL, algorithm, source.FileName)
				if err != nil {
					return nil, fmt.Errorf("download %s source %s checksum: %w", tool, source.Name, err)
				}
				checksum = published
			}
			if err := verifyFileChecksum(algorithm, checksum, archivePath); err != nil {
				return nil, fmt.Errorf("verify %s source %s: %w", tool, source.Name, err)
			}
		}

		infos = append(infos, BuildSourceInfo{
			Name:              source.Name,
			URL:               source.URL,
			FileName:          source.FileName,
			ChecksumAlgorithm: string(algorithm),
			Checksum:          checksum,
			Unverified:        source.Unverified,
			archiveFormat:     source.ArchiveFormat,
		})
	}

	return infos, nil
}

// buildRun holds the state shared by a single build's steps.
type buildRun struct {
	tool     string
	label    string
	manifest pluginManifest
	request  BuildRequest
	jobs     int
	log      io.Writer
	logPath  string
	root     string
	prefix   string
	env      []string
}

// depsDir holds libraries private to the build, such as Apache's static APR.
func (r buildRun) depsDir() string {
	return filepath.Join(r.root, "deps")
}

// stagingDir is the destination directory make install writes under.
func (r buildRun) stagingDir() string {
	return filepath.Join(r.root, "staging")
}

// payloadDir is where the compiled prefix lands inside the staging directory.
func (r buildRun) payloadDir() string {
	return filepath.Join(r.stagingDir(), filepath.FromSlash(strings.TrimPrefix(r.prefix, "/")))
}

// extractSources unpacks each verified source into its own directory.
func (r buildRun) extractSources(sources []BuildSourceInfo) (map[string]string, error) {
	directories := make(map[string]string, len(sources))
	for _, source := range sources {
		target := filepath.Join(r.root, "src", source.Name)
		archivePath := filepath.Join(r.root, "downloads", source.FileName)
		if err := extractSourceArchive(archivePath, target, source.archiveFormat); err != nil {
			return nil, fmt.Errorf("extract %s source %s: %w", r.tool, source.Name, err)
		}
		if err := collapseSingleDirectory(target); err != nil {
			return nil, fmt.Errorf("extract %s source %s: %w", r.tool, source.Name, err)
		}
		directories[source.Name] = target
	}

	return directories, nil
}

// runStage runs one stage's configure, make, and install.
func (r buildRun) runStage(ctx context.Context, index int, stage manifestBuildStage, sourceDir string) error {
	name := strings.TrimSpace(stage.Name)
	r.request.status("compiling %s (%s)", r.tool, name)

	prefix := r.prefix
	toDeps := strings.TrimSpace(stage.Prefix) == buildPrefixDeps
	if toDeps {
		prefix = filepath.ToSlash(r.depsDir())
	}
	placeholders := buildStagePlaceholders(prefix, filepath.ToSlash(r.depsDir()), r.jobs)
	render := func(values []string) ([]string, error) {
		rendered := make([]string, 0, len(values))
		for _, value := range values {
			text, err := renderDownloadTemplate("build stage "+name, value, placeholders)
			if err != nil {
				return nil, err
			}
			rendered = append(rendered, text)
		}
		return rendered, nil
	}

	env := append([]string(nil), r.env...)
	envNames := make([]string, 0, len(stage.Env))
	for key := range stage.Env {
		envNames = append(envNames, key)
	}
	slices.Sort(envNames)
	for _, key := range envNames {
		value, err := renderDownloadTemplate("build stage "+name, stage.Env[key], placeholders)
		if err != nil {
			return err
		}
		env = append(env, key+"="+value)
	}

	if len(stage.Configure) > 0 {
		args, err := render(stage.Configure)
		if err != nil {
			return err
		}
		if stage.DetectLibdir {
			if libdir := detectBuildLibdir(ctx); libdir != "" {
				args = append(args, "--with-libdir="+libdir)
			}
		}
		// User arguments come last, because later options win in autoconf.
		if index == r.manifest.Build.configureStageIndex() {
			args = append(args, r.request.ConfigureArgs...)
		}
		if err := r.run(ctx, name, buildCommand{Dir: sourceDir, Target: "./configure", Args: args, Env: env}); err != nil {
			return err
		}
	}

	makeArgs, err := render(stage.Make)
	if err != nil {
		return err
	}
	makeArgs = append([]string{fmt.Sprintf("-j%d", r.jobs)}, makeArgs...)
	if err := r.run(ctx, name, buildCommand{Dir: sourceDir, Target: "make", Args: makeArgs, Env: env}); err != nil {
		return err
	}

	if len(stage.Install) == 0 {
		return nil
	}
	installArgs, err := render(stage.Install)
	if err != nil {
		return err
	}
	if !toDeps {
		destdirVar := strings.TrimSpace(stage.DestdirVar)
		if destdirVar == "" {
			destdirVar = "DESTDIR"
		}
		installArgs = append(installArgs, destdirVar+"="+filepath.ToSlash(r.stagingDir()))
	}

	return r.run(ctx, name, buildCommand{Dir: sourceDir, Target: "make", Args: installArgs, Env: env})
}

// run executes a command, recording it in the log and converting a failure into
// an error carrying the log's tail.
func (r buildRun) run(ctx context.Context, stage string, command buildCommand) error {
	if _, err := fmt.Fprintf(r.log, "\n$ (%s) %s %s\n", command.Dir, command.Target, strings.Join(command.Args, " ")); err != nil {
		return fmt.Errorf("write build log: %w", err)
	}
	err := buildCommandRunner(ctx, command, r.log)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	tail, tailErr := buildLogTail(r.logPath, buildLogTailLines)
	if tailErr != nil {
		err = errors.Join(err, tailErr)
	}

	return &BuildStageError{Tool: r.tool, Stage: stage, LogPath: r.logPath, Tail: tail, Err: err}
}

// packageAndVerify builds the payload archive, then proves it by doing what
// install does: extract and flatten it somewhere else, find the executable the
// manifest promises, and run the recipe's checks from an unrelated directory.
// That catches relocation and flattening problems here rather than in a project.
func (r buildRun) packageAndVerify(ctx context.Context, sourceDir, version, goos, goarch string) (string, error) {
	spec := r.manifest.Build.Package
	if err := applyBuildPackage(spec, sourceDir, r.payloadDir(), r.prefix); err != nil {
		return "", fmt.Errorf("package %s: %w", r.tool, err)
	}

	fileName := fmt.Sprintf("%s-%s-%s-build.tar.gz", r.tool, version, platformKey(goos, goarch))
	archivePath := filepath.Join(r.root, fileName)
	topLevel := r.tool + "-" + version
	if err := packBuildPayload(r.payloadDir(), topLevel, r.prefix, archivePath); err != nil {
		return "", fmt.Errorf("package %s: %w", r.tool, err)
	}

	verifyDir := filepath.Join(r.root, "verify")
	if err := extractArchive(archivePath, verifyDir, archiveFormatTarGz); err != nil {
		return "", fmt.Errorf("verify %s payload: %w", r.tool, err)
	}
	if err := collapseSingleDirectory(verifyDir); err != nil {
		return "", fmt.Errorf("verify %s payload: %w", r.tool, err)
	}

	candidates := manifestCandidatePaths(verifyDir, r.manifest.InstallCandidates, goos, goarch)
	found := false
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("verify %s payload: no install candidate exists among %s", r.tool, strings.Join(selectManifestPlatformPaths(r.manifest.InstallCandidates, goos, goarch), ", "))
	}

	r.request.status("verifying %s", r.tool)
	for _, relative := range r.manifest.Build.VerifyFiles {
		if _, err := os.Stat(buildPackagePath(verifyDir, relative)); err != nil {
			return "", fmt.Errorf("verify %s payload: expected file %s: %w", r.tool, relative, err)
		}
	}
	for _, check := range r.manifest.Build.Verify {
		args := make([]string, 0, len(check)-1)
		for _, arg := range check[1:] {
			args = append(args, strings.ReplaceAll(arg, "{install}", filepath.ToSlash(verifyDir)))
		}
		command := buildCommand{
			Dir:    r.root,
			Target: filepath.Join(verifyDir, filepath.FromSlash(check[0])),
			Args:   args,
			Env:    r.env,
		}
		if err := r.run(ctx, "verify", command); err != nil {
			return "", err
		}
	}

	return archivePath, nil
}

// builtPayloadSourceURL is the cache entry's source for a local build.
func builtPayloadSourceURL(info BuildInfo) string {
	if len(info.Sources) == 0 {
		return builtPayloadSourcePrefix
	}

	return builtPayloadSourcePrefix + info.Sources[len(info.Sources)-1].URL
}
