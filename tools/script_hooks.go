package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"go.starlark.net/starlark"

	"polka/config"
	"polka/script"
)

// Hook names a manifest script may implement.
const (
	scriptHookResolve      = "resolve"
	scriptHookValidate     = "validate"
	scriptHookDependencies = "dependencies"
	scriptHookPostInstall  = "post_install"
)

// scriptLibraryDir holds script code shared through load(), relative to the
// manifest directory. load() names resolve inside it.
const scriptLibraryDir = "scripts/lib"

// downloadTemplateKey matches names usable as {placeholder} template values.
var downloadTemplateKey = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// scriptResolveReservedValues are built-in download template placeholders a
// resolve hook may not override through its values map.
var scriptResolveReservedValues = map[string]bool{
	"version":     true,
	"requested":   true,
	"tag":         true,
	"major":       true,
	"minor":       true,
	"major_minor": true,
}

// manifestScript declares a sandboxed Starlark script implementing selected
// plugin hooks. File is relative to the manifest's directory.
type manifestScript struct {
	File  string            `yaml:"file"`
	Hooks []string          `yaml:"hooks"`
	Vars  map[string]string `yaml:"vars"`
}

// scriptModuleLoader lazily loads and binds a manifest's script module.
type scriptModuleLoader func() (*script.Module, error)

var (
	builtinScriptMu      sync.Mutex
	builtinScriptLoaders = map[string]scriptModuleLoader{}
)

// isZero reports whether the manifest declares no script block.
func (s manifestScript) isZero() bool {
	return strings.TrimSpace(s.File) == "" && len(s.Hooks) == 0 && len(s.Vars) == 0
}

// hasHook reports whether the script declares hook.
func (s manifestScript) hasHook(hook string) bool {
	for _, declared := range s.Hooks {
		if declared == hook {
			return true
		}
	}

	return false
}

// normalized returns the script block with a cleaned slash path, lowercased
// hook names, and lowercased variable keys.
func (s manifestScript) normalized() manifestScript {
	if s.isZero() {
		return manifestScript{}
	}
	normalized := manifestScript{}
	if file := strings.TrimSpace(s.File); file != "" {
		normalized.File = filepath.ToSlash(filepath.Clean(filepath.FromSlash(file)))
	}
	for _, hook := range s.Hooks {
		normalized.Hooks = append(normalized.Hooks, strings.ToLower(strings.TrimSpace(hook)))
	}
	if len(s.Vars) > 0 {
		normalized.Vars = make(map[string]string, len(s.Vars))
		for key, value := range s.Vars {
			normalized.Vars[strings.ToLower(strings.TrimSpace(key))] = value
		}
	}

	return normalized
}

// validate checks the script block against the rest of the manifest.
func (s manifestScript) validate(id string, download manifestDownload, dependencies []manifestDependency) error {
	if s.isZero() {
		return nil
	}
	file := strings.TrimSpace(s.File)
	if file == "" {
		return fmt.Errorf("tool manifest %q script requires file", id)
	}
	if err := validateManifestContainedPath(file, "script file", "manifest directory"); err != nil {
		return fmt.Errorf("tool manifest %q script: %w", id, err)
	}
	// filepath.IsAbs misses rooted paths such as /x.star on Windows, so also
	// require a valid fs.FS path, which rejects any leading slash.
	if !fs.ValidPath(filepath.ToSlash(filepath.Clean(filepath.FromSlash(file)))) {
		return fmt.Errorf("tool manifest %q script file %q must be relative to the manifest directory", id, s.File)
	}
	if path.Ext(filepath.ToSlash(file)) != ".star" {
		return fmt.Errorf("tool manifest %q script file %q must use the .star extension", id, s.File)
	}
	if len(s.Hooks) == 0 {
		return fmt.Errorf("tool manifest %q script requires at least one hook", id)
	}
	seen := map[string]bool{}
	for index, rawHook := range s.Hooks {
		hook := strings.ToLower(strings.TrimSpace(rawHook))
		switch hook {
		case scriptHookResolve, scriptHookValidate, scriptHookDependencies, scriptHookPostInstall:
		default:
			return fmt.Errorf("tool manifest %q script.hooks[%d] has unsupported hook %q", id, index, rawHook)
		}
		if seen[hook] {
			return fmt.Errorf("tool manifest %q script.hooks[%d] duplicates hook %q", id, index, hook)
		}
		seen[hook] = true
	}
	for key := range s.Vars {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if normalizedKey == "" || !validName.MatchString(normalizedKey) {
			return fmt.Errorf("tool manifest %q has invalid script.vars key %q", id, key)
		}
	}
	if seen[scriptHookResolve] {
		if len(download.Assets) == 0 {
			return fmt.Errorf("tool manifest %q script hook resolve requires download.assets", id)
		}
		if download.hasGitHub() || download.hasReleaseIndex() {
			return fmt.Errorf("tool manifest %q script hook resolve cannot be combined with download.github or download.release-index", id)
		}
	}
	if seen[scriptHookDependencies] && len(dependencies) > 0 {
		return fmt.Errorf("tool manifest %q script hook dependencies cannot be combined with manifest dependencies", id)
	}

	return nil
}

// newScriptModuleLoader returns a loader that compiles the manifest's script
// from fsys on first use and caches the result for the loader's lifetime.
func newScriptModuleLoader(fsys fs.FS, tool string, spec manifestScript) (scriptModuleLoader, error) {
	if _, err := fs.Stat(fsys, spec.File); err != nil {
		return nil, fmt.Errorf("tool manifest %q script: %w", tool, err)
	}

	return sync.OnceValues(func() (*script.Module, error) {
		return loadToolScript(fsys, tool, spec)
	}), nil
}

// builtinScriptModuleLoader shares one loader per built-in tool script across
// every registry built in this process, so repeated NewDefaultRegistry calls
// on the dispatch path never re-parse Starlark. candidate is registered when
// no loader is cached yet for the tool's script.
func builtinScriptModuleLoader(tool string, spec manifestScript, candidate scriptModuleLoader) scriptModuleLoader {
	builtinScriptMu.Lock()
	defer builtinScriptMu.Unlock()

	key := tool + "\x00" + spec.File
	if loader, ok := builtinScriptLoaders[key]; ok {
		return loader
	}
	builtinScriptLoaders[key] = candidate

	return candidate
}

// loadToolScript loads a tool script and checks that its public functions
// match the hooks declared in the manifest.
func loadToolScript(fsys fs.FS, tool string, spec manifestScript) (*script.Module, error) {
	module, err := script.Load(fsys, spec.File, script.Options{Predeclared: toolScriptModules(), LoadDir: scriptLibraryDir})
	if err != nil {
		return nil, fmt.Errorf("%s script: %w", tool, err)
	}

	for _, name := range module.Names() {
		if strings.HasPrefix(name, "_") {
			continue
		}
		if _, callable := module.Global(name).(starlark.Callable); !callable {
			continue
		}
		switch name {
		case scriptHookResolve, scriptHookValidate, scriptHookDependencies, scriptHookPostInstall:
			if !spec.hasHook(name) {
				return nil, fmt.Errorf("%s script %s defines hook %q that is not declared in script.hooks", tool, spec.File, name)
			}
		default:
			return nil, fmt.Errorf("%s script %s defines unsupported public function %q; prefix helpers with _", tool, spec.File, name)
		}
	}
	for _, hook := range spec.Hooks {
		fn, ok := module.Global(hook).(*starlark.Function)
		if !ok {
			return nil, fmt.Errorf("%s script %s declares hook %q but does not define it as a function", tool, spec.File, hook)
		}
		if fn.NumParams() != 1 || fn.HasVarargs() || fn.HasKwargs() || fn.NumKwonlyParams() != 0 {
			return nil, fmt.Errorf("%s script %s hook %q must take exactly one parameter", tool, spec.File, hook)
		}
	}

	return module, nil
}

// scriptModule returns the manifest's loaded script module.
func (m pluginManifest) scriptModule() (*script.Module, error) {
	if m.loadScript == nil {
		return nil, fmt.Errorf("tool manifest %q script %s was not loaded from a manifest filesystem", m.ID, m.Script.File)
	}

	return m.loadScript()
}

// scriptHookError converts a hook failure into a polka error. fail() messages
// are user-facing and returned verbatim; anything else is a plugin bug and is
// reported with its Starlark backtrace.
func scriptHookError(tool, file string, err error) error {
	var failErr *script.FailError
	if errors.As(err, &failErr) {
		return failErr
	}

	return fmt.Errorf("%s script %s: %s", tool, file, script.Describe(err))
}

// scriptResolvePlan is the validated result of a resolve hook.
type scriptResolvePlan struct {
	Version           string
	Tag               string
	Values            map[string]string
	URL               string
	Checksum          string
	ChecksumURL       string
	ChecksumAlgorithm string
}

// resolveScriptRequest runs the manifest's resolve hook and turns its plan
// into a rendered download asset. The download itself, checksum verification,
// and caching stay in Go.
func resolveScriptRequest(client *http.Client, m pluginManifest, requestedVersion, goos, goarch string) (manifestRequestPlan, error) {
	tool := strings.ToLower(strings.TrimSpace(m.ID))
	requestedVersion = strings.TrimSpace(requestedVersion)
	if requestedVersion == "" {
		return manifestRequestPlan{}, fmt.Errorf("%s version cannot be empty", tool)
	}
	module, err := m.scriptModule()
	if err != nil {
		return manifestRequestPlan{}, err
	}

	ctx := script.NewStruct("ctx", starlark.StringDict{
		"tool":      starlark.String(tool),
		"requested": starlark.String(requestedVersion),
		"os":        starlark.String(goos),
		"arch":      starlark.String(goarch),
		"platform":  starlark.String(platformKey(goos, goarch)),
		"vars":      script.StringMap(m.Script.Vars),
	})
	locals := map[string]any{scriptHTTPLocal: scriptHTTPAccess{client: client, tool: tool}}
	result, err := module.Call(scriptHookResolve, locals, ctx)
	if err != nil {
		return manifestRequestPlan{}, scriptHookError(tool, m.Script.File, err)
	}
	plan, err := parseScriptResolvePlan(result)
	if err != nil {
		return manifestRequestPlan{}, fmt.Errorf("%s script %s resolve: %w", tool, m.Script.File, err)
	}

	return plan.toRequestPlan(tool, m, requestedVersion, goos, goarch)
}

// parseScriptResolvePlan converts a resolve hook result dict into a plan,
// rejecting unknown keys and non-string values.
func parseScriptResolvePlan(result starlark.Value) (scriptResolvePlan, error) {
	dict, ok := result.(*starlark.Dict)
	if !ok {
		return scriptResolvePlan{}, fmt.Errorf("result must be a dict, got %s", result.Type())
	}

	plan := scriptResolvePlan{}
	stringFields := map[string]*string{
		"version":            &plan.Version,
		"tag":                &plan.Tag,
		"url":                &plan.URL,
		"checksum":           &plan.Checksum,
		"checksum_url":       &plan.ChecksumURL,
		"checksum_algorithm": &plan.ChecksumAlgorithm,
	}
	for _, item := range dict.Items() {
		key, ok := starlark.AsString(item[0])
		if !ok {
			return scriptResolvePlan{}, fmt.Errorf("result keys must be strings, got %s", item[0].Type())
		}
		if key == "values" {
			values, err := script.ToStringMap(item[1], "result values")
			if err != nil {
				return scriptResolvePlan{}, err
			}
			plan.Values = values
			continue
		}
		target, ok := stringFields[key]
		if !ok {
			return scriptResolvePlan{}, fmt.Errorf("result has unsupported key %q", key)
		}
		if item[1] == starlark.None {
			continue
		}
		text, ok := starlark.AsString(item[1])
		if !ok {
			return scriptResolvePlan{}, fmt.Errorf("result %s must be a string, got %s", key, item[1].Type())
		}
		*target = strings.TrimSpace(text)
	}
	if plan.Version == "" {
		return scriptResolvePlan{}, fmt.Errorf("result requires version")
	}

	return plan, nil
}

// toRequestPlan validates the plan and renders the manifest asset with the
// plan's values and overrides applied.
func (p scriptResolvePlan) toRequestPlan(tool string, m pluginManifest, requestedVersion, goos, goarch string) (manifestRequestPlan, error) {
	// The resolved version names cache and install directories, so it must
	// pass the same validation as user-supplied labels.
	if err := validateVersion(tool, p.Version); err != nil {
		return manifestRequestPlan{}, err
	}
	if p.Tag != "" {
		if err := validateVersion(tool, p.Tag); err != nil {
			return manifestRequestPlan{}, fmt.Errorf("%s script tag: %w", tool, err)
		}
	}
	if !m.VersionConstraint.allowsResolved(p.Version) {
		return manifestRequestPlan{}, m.VersionConstraint.error(tool, p.Version)
	}
	for key := range p.Values {
		if scriptResolveReservedValues[key] {
			return manifestRequestPlan{}, fmt.Errorf("%s script resolve values cannot override built-in placeholder %q", tool, key)
		}
		if !downloadTemplateKey.MatchString(key) {
			return manifestRequestPlan{}, fmt.Errorf("%s script resolve values key %q must use letters, numbers, or underscores", tool, key)
		}
	}

	asset, err := resolveManifestDownloadAsset(tool, m.Download.Assets, requestedVersion, p.Version, p.Tag, goos, goarch, p.Values)
	if err != nil {
		return manifestRequestPlan{}, err
	}
	// Plan values often come from upstream metadata and these names become
	// staging paths, so they must stay plain file names.
	for _, name := range []string{asset.FileName, asset.Unwrap.FileName} {
		if strings.ContainsAny(name, `/\`) || name == ".." {
			return manifestRequestPlan{}, fmt.Errorf("%s script resolve rendered invalid file name %q", tool, name)
		}
	}
	if p.URL != "" {
		if err := validateScriptURL(p.URL); err != nil {
			return manifestRequestPlan{}, fmt.Errorf("%s script resolve url: %w", tool, err)
		}
		asset.URL = p.URL
	}
	if p.ChecksumURL != "" {
		if err := validateScriptURL(p.ChecksumURL); err != nil {
			return manifestRequestPlan{}, fmt.Errorf("%s script resolve checksum_url: %w", tool, err)
		}
		asset.ChecksumURL = p.ChecksumURL
	}
	if p.ChecksumAlgorithm != "" {
		switch algorithm := checksumAlgorithm(strings.ToLower(p.ChecksumAlgorithm)); algorithm {
		case checksumAlgorithmMD5, checksumAlgorithmSHA256, checksumAlgorithmSHA3_256:
			asset.ChecksumAlgorithm = algorithm
		default:
			return manifestRequestPlan{}, fmt.Errorf("%s script resolve has unsupported checksum_algorithm %q", tool, p.ChecksumAlgorithm)
		}
	}
	if p.Checksum != "" {
		if asset.ChecksumAlgorithm == checksumAlgorithmNone {
			return manifestRequestPlan{}, fmt.Errorf("%s script resolve checksum requires a checksum algorithm", tool)
		}
		checksum, _, err := validateChecksumValue(asset.ChecksumAlgorithm, p.Checksum)
		if err != nil {
			return manifestRequestPlan{}, fmt.Errorf("%s script resolve checksum: %w", tool, err)
		}
		asset.Checksum = checksum
	}

	tag := p.Tag
	if tag == "" {
		tag = p.Version
	}

	return manifestRequestPlan{ResolvedVersion: p.Version, Tag: tag, Asset: asset}, nil
}

// validateScriptURL requires an absolute http or https URL.
func validateScriptURL(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%q must be an absolute http or https URL", rawURL)
	}

	return nil
}

// scriptValidateFunc adapts the manifest's validate hook to a plugin
// validator. The hook only runs when the tool has a version or settings, so
// commands that never touch the tool do not compile its script.
func scriptValidateFunc(m pluginManifest, settingsNamespace string, settings map[string]SettingType, rawVersion func(config.Environment) string) func(config.Environment) error {
	tool := strings.ToLower(strings.TrimSpace(m.ID))

	return func(environment config.Environment) error {
		version := strings.TrimSpace(rawVersion(environment))
		if version == "" && !settingsConfigured(environment, settingsNamespace, settings) {
			return nil
		}
		module, err := m.scriptModule()
		if err != nil {
			return err
		}

		env := newScriptEnvironment(m, tool, version, settingsNamespace, settings, runtime.GOOS, runtime.GOARCH, environment)
		result, err := module.Call(scriptHookValidate, nil, env)
		if err != nil {
			return scriptHookError(tool, m.Script.File, err)
		}
		if result != starlark.None {
			return fmt.Errorf("%s script %s validate must return None; use fail(msg) to report errors", tool, m.Script.File)
		}

		return nil
	}
}

// scriptDependenciesFunc adapts the manifest's dependencies hook. The hook only
// runs for a tool with a configured version, so commands that never touch the
// tool do not compile its script.
func scriptDependenciesFunc(m pluginManifest, settingsNamespace string, settings map[string]SettingType, rawVersion func(config.Environment) string, goos, goarch string) func(config.Environment) ([]InstallRequest, error) {
	tool := strings.ToLower(strings.TrimSpace(m.ID))

	return func(environment config.Environment) ([]InstallRequest, error) {
		version := strings.TrimSpace(rawVersion(environment))
		if version == "" {
			return nil, nil
		}
		module, err := m.scriptModule()
		if err != nil {
			return nil, err
		}

		env := newScriptEnvironment(m, tool, version, settingsNamespace, settings, goos, goarch, environment)
		result, err := module.Call(scriptHookDependencies, nil, env)
		if err != nil {
			return nil, scriptHookError(tool, m.Script.File, err)
		}
		requests, err := parseScriptDependencies(tool, result)
		if err != nil {
			return nil, fmt.Errorf("%s script %s dependencies: %w", tool, m.Script.File, err)
		}

		return requests, nil
	}
}

// parseScriptDependencies converts a dependencies hook result, a list of
// {"tool": ..., "version": ...} dicts, into install requests.
func parseScriptDependencies(tool string, result starlark.Value) ([]InstallRequest, error) {
	list, ok := result.(*starlark.List)
	if !ok {
		return nil, fmt.Errorf("result must be a list, got %s", result.Type())
	}

	var requests []InstallRequest
	for index := range list.Len() {
		dict, ok := list.Index(index).(*starlark.Dict)
		if !ok {
			return nil, fmt.Errorf("result[%d] must be a dict, got %s", index, list.Index(index).Type())
		}
		request := InstallRequest{}
		for _, item := range dict.Items() {
			// A non-string key reads as "" and is rejected by the default case.
			key, _ := starlark.AsString(item[0])
			text, isString := starlark.AsString(item[1])
			switch key {
			case "tool", "version":
				if !isString {
					return nil, fmt.Errorf("result[%d] %s must be a string, got %s", index, key, item[1].Type())
				}
				if key == "tool" {
					request.Tool = strings.ToLower(strings.TrimSpace(text))
				} else {
					request.Version = strings.TrimSpace(text)
				}
			default:
				return nil, fmt.Errorf("result[%d] has unsupported key %s", index, item[0])
			}
		}
		if !validName.MatchString(request.Tool) {
			return nil, fmt.Errorf("result[%d] has invalid tool %q", index, request.Tool)
		}
		if request.Tool == tool {
			return nil, fmt.Errorf("result[%d] cannot depend on %s itself", index, tool)
		}
		if err := validateVersion(request.Tool, request.Version); err != nil {
			return nil, fmt.Errorf("result[%d]: %w", index, err)
		}
		requests = append(requests, request)
	}

	return requests, nil
}

// newScriptEnvironment builds the read-only env value passed to the validate
// and dependencies hooks.
func newScriptEnvironment(m pluginManifest, tool, version, settingsNamespace string, settings map[string]SettingType, goos, goarch string, environment config.Environment) starlark.Value {
	toolVersion := starlark.NewBuiltin("env.tool_version", func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var id string
		if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &id); err != nil {
			return nil, err
		}

		return starlark.String(environment.ToolVersion(id)), nil
	})
	setting := starlark.NewBuiltin("env.setting", func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var rawKey string
		if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &rawKey); err != nil {
			return nil, err
		}
		key := strings.ToLower(strings.TrimSpace(rawKey))
		switch settings[key] {
		case SettingTypePort:
			return starlark.MakeInt(environment.SettingInt(settingsNamespace, key)), nil
		case SettingTypeString:
			return starlark.String(environment.SettingString(settingsNamespace, key)), nil
		case SettingTypeBool:
			return starlark.Bool(environment.SettingBool(settingsNamespace, key)), nil
		default:
			declared := make([]string, 0, len(settings))
			for name := range settings {
				declared = append(declared, name)
			}
			sort.Strings(declared)
			return nil, fmt.Errorf("%s: %s declares no setting %q (declared: %s)", b.Name(), tool, rawKey, strings.Join(declared, ", "))
		}
	})

	return script.NewStruct("env", starlark.StringDict{
		"version":      starlark.String(version),
		"os":           starlark.String(goos),
		"arch":         starlark.String(goarch),
		"platform":     starlark.String(platformKey(goos, goarch)),
		"vars":         script.StringMap(m.Script.Vars),
		"tool_version": toolVersion,
		"setting":      setting,
	})
}
