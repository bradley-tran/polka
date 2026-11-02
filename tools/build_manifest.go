package tools

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Prefix kinds a build stage may install into.
const (
	// buildPrefixPayload installs through a destination directory into the
	// tree that becomes the cached payload. It is the default.
	buildPrefixPayload = "payload"
	// buildPrefixDeps installs directly into a private directory the later
	// stages link against, such as Apache's static APR.
	buildPrefixDeps = "deps"
)

// validBuildCommandName matches an executable name. It is wider than validName
// because compilers such as c++ use characters tool IDs cannot.
var validBuildCommandName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// manifestBuild is the recipe `polka build` follows to compile a tool from
// source. Scripts locate sources through the build_sources hook; everything the
// compile does is declared here, so Go performs every action.
type manifestBuild struct {
	// From reuses the build block of another manifest, as settings-from does
	// for settings. It excludes every field except ExtraConfigure.
	From string `yaml:"from"`
	// ExtraConfigure extends the final configure stage of a From recipe.
	ExtraConfigure []string `yaml:"extra-configure"`
	// Platforms lists the os-arch keys the recipe can target.
	Platforms []string `yaml:"platforms"`
	// MinVersion rejects resolved versions older than this before compiling.
	MinVersion string                `yaml:"min-version"`
	Requires   manifestBuildRequires `yaml:"requires"`
	Stages     []manifestBuildStage  `yaml:"stages"`
	Package    manifestBuildPackage  `yaml:"package"`
	// Verify lists commands that must succeed against the extracted payload.
	// The first element is a path relative to the install directory.
	Verify [][]string `yaml:"verify"`
	// VerifyFiles lists payload files that must exist. A tool can start and
	// still be missing a part that only loads later, such as an Apache module.
	VerifyFiles []string `yaml:"verify-files"`
}

// manifestBuildRequires lists host prerequisites checked before any download,
// so a user sees every missing item at once rather than one per failed compile.
type manifestBuildRequires struct {
	// Commands are required executables; "cc|gcc" accepts either.
	Commands  []string               `yaml:"commands"`
	Libraries []manifestBuildLibrary `yaml:"libraries"`
}

// manifestBuildLibrary is one host development library, detected through
// pkg-config, a header, or both (either detection satisfies it).
type manifestBuildLibrary struct {
	Name      string `yaml:"name"`
	PkgConfig string `yaml:"pkg-config"`
	Header    string `yaml:"header"`
	Hint      string `yaml:"hint"`
	// SkipWith lists --configure-arg values that remove the need for this
	// library, such as --without-tidy.
	SkipWith []string `yaml:"skip-with"`
}

// manifestBuildStage compiles one source tree: an optional configure, make, and
// an optional install. Arguments and environment values may use the {prefix},
// {deps}, and {jobs} placeholders.
type manifestBuildStage struct {
	Name string `yaml:"name"`
	// Source names an entry returned by the build_sources hook.
	Source string `yaml:"source"`
	// Prefix selects the install location: payload (default) or deps.
	Prefix string `yaml:"prefix"`
	// Configure holds ./configure arguments; empty skips configure.
	Configure []string          `yaml:"configure"`
	Env       map[string]string `yaml:"env"`
	// DetectLibdir appends --with-libdir when the host keeps libraries outside
	// plain lib, which PHP's configure cannot find on its own.
	DetectLibdir bool `yaml:"detect-libdir"`
	// Make holds extra make arguments for the compile step.
	Make []string `yaml:"make"`
	// Install lists make targets for the install step; empty skips it.
	Install []string `yaml:"install"`
	// DestdirVar names the make variable for the destination directory.
	// PHP uses INSTALL_ROOT; everything else uses the default DESTDIR.
	DestdirVar string `yaml:"destdir-var"`
}

// manifestBuildPackage shapes the installed tree into the cached payload.
type manifestBuildPackage struct {
	// Copy places files from the final stage's source tree into the payload,
	// for tools such as Redis with no install layout worth keeping.
	Copy []manifestBuildCopy `yaml:"copy"`
	// Remove deletes payload paths that do not survive relocation or that no
	// Polka feature uses.
	Remove []string `yaml:"remove"`
	// KeepDirs creates payload directories the tool expects to exist.
	KeepDirs []string `yaml:"keep-dirs"`
	// RelocateScripts lists shell scripts rewritten to compute the build prefix
	// from their own location, because payloads install to per-project paths.
	RelocateScripts []string `yaml:"relocate-scripts"`
}

// manifestBuildCopy copies one file; both paths are relative to their root.
type manifestBuildCopy struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// isZero reports whether the manifest declares no build block.
func (b manifestBuild) isZero() bool {
	return strings.TrimSpace(b.From) == "" && len(b.ExtraConfigure) == 0 && len(b.Platforms) == 0 &&
		strings.TrimSpace(b.MinVersion) == "" && len(b.Requires.Commands) == 0 && len(b.Requires.Libraries) == 0 &&
		len(b.Stages) == 0 && len(b.Package.Copy) == 0 && len(b.Package.Remove) == 0 &&
		len(b.Package.KeepDirs) == 0 && len(b.Package.RelocateScripts) == 0 && len(b.Verify) == 0 && len(b.VerifyFiles) == 0
}

// supports reports whether the recipe can target the given platform.
func (b manifestBuild) supports(goos, goarch string) bool {
	if len(b.Stages) == 0 {
		return false
	}
	want := platformKey(goos, goarch)
	for _, platform := range b.Platforms {
		if strings.TrimSpace(platform) == want {
			return true
		}
	}

	return false
}

// configureStageIndex returns the last stage that runs configure, which is the
// stage --configure-arg extends, or -1 for a recipe with none.
func (b manifestBuild) configureStageIndex() int {
	for index := len(b.Stages) - 1; index >= 0; index-- {
		if len(b.Stages[index].Configure) > 0 {
			return index
		}
	}

	return -1
}

// validate checks the build block's own structure. A From recipe is checked
// once loadManifest has merged the referenced block.
func (b manifestBuild) validate(id string, scriptBlock manifestScript) error {
	if b.isZero() {
		if scriptBlock.declaresHook(scriptHookBuildSources) {
			return fmt.Errorf("tool manifest %q script hook %s requires a build block", id, scriptHookBuildSources)
		}
		return nil
	}
	if !scriptBlock.declaresHook(scriptHookBuildSources) {
		return fmt.Errorf("tool manifest %q build requires script hook %s", id, scriptHookBuildSources)
	}

	if from := strings.ToLower(strings.TrimSpace(b.From)); from != "" {
		if !validName.MatchString(from) {
			return fmt.Errorf("tool manifest %q has invalid build.from %q", id, b.From)
		}
		if from == id {
			return fmt.Errorf("tool manifest %q build.from cannot reference itself", id)
		}
		merged := b
		merged.From = ""
		merged.ExtraConfigure = nil
		if !merged.isZero() {
			return fmt.Errorf("tool manifest %q build.from cannot be combined with other build fields except extra-configure", id)
		}
		return nil
	}
	if len(b.ExtraConfigure) > 0 {
		return fmt.Errorf("tool manifest %q build.extra-configure requires build.from", id)
	}

	return b.validateRecipe(id)
}

// validateRecipe checks a complete recipe, including one merged through from.
func (b manifestBuild) validateRecipe(id string) error {
	if len(b.Platforms) == 0 {
		return fmt.Errorf("tool manifest %q build requires platforms", id)
	}
	for index, platform := range b.Platforms {
		if !validDownloadPlatform(strings.TrimSpace(platform)) {
			return fmt.Errorf("tool manifest %q build.platforms[%d] has unsupported platform %q", id, index, platform)
		}
	}
	if minVersion := strings.TrimSpace(b.MinVersion); minVersion != "" {
		if err := validateVersion(id, minVersion); err != nil {
			return fmt.Errorf("tool manifest %q build.min-version: %w", id, err)
		}
	}
	for index, command := range b.Requires.Commands {
		for _, alternative := range strings.Split(command, "|") {
			if !validBuildCommandName.MatchString(strings.TrimSpace(alternative)) {
				return fmt.Errorf("tool manifest %q build.requires.commands[%d] has invalid command %q", id, index, command)
			}
		}
	}
	for index, library := range b.Requires.Libraries {
		if strings.TrimSpace(library.Name) == "" {
			return fmt.Errorf("tool manifest %q build.requires.libraries[%d] requires name", id, index)
		}
		if strings.TrimSpace(library.PkgConfig) == "" && strings.TrimSpace(library.Header) == "" {
			return fmt.Errorf("tool manifest %q build.requires.libraries[%d] requires pkg-config or header", id, index)
		}
	}
	if len(b.Stages) == 0 {
		return fmt.Errorf("tool manifest %q build requires stages", id)
	}
	seenStages := map[string]bool{}
	for index, stage := range b.Stages {
		if err := stage.validate(id, index, seenStages); err != nil {
			return err
		}
	}
	for index, entry := range b.Package.Copy {
		for _, candidate := range []string{entry.From, entry.To} {
			if err := validateManifestContainedPath(candidate, "copy path", "package tree"); err != nil {
				return fmt.Errorf("tool manifest %q build.package.copy[%d]: %w", id, index, err)
			}
		}
	}
	for field, paths := range map[string][]string{
		"package.remove":           b.Package.Remove,
		"package.keep-dirs":        b.Package.KeepDirs,
		"package.relocate-scripts": b.Package.RelocateScripts,
		"verify-files":             b.VerifyFiles,
	} {
		for index, candidate := range paths {
			if err := validateManifestContainedPath(candidate, "package path", "package tree"); err != nil {
				return fmt.Errorf("tool manifest %q build.%s[%d]: %w", id, field, index, err)
			}
		}
	}
	for index, command := range b.Verify {
		if len(command) == 0 {
			return fmt.Errorf("tool manifest %q build.verify[%d] requires a command", id, index)
		}
		if err := validateManifestContainedPath(command[0], "verify command", "install directory"); err != nil {
			return fmt.Errorf("tool manifest %q build.verify[%d]: %w", id, index, err)
		}
	}

	return nil
}

// validate checks one stage, rejecting placeholders that cannot be rendered.
func (s manifestBuildStage) validate(id string, index int, seen map[string]bool) error {
	name := strings.TrimSpace(s.Name)
	if name == "" || !validName.MatchString(name) {
		return fmt.Errorf("tool manifest %q build.stages[%d] has invalid name %q", id, index, s.Name)
	}
	if seen[name] {
		return fmt.Errorf("tool manifest %q build.stages[%d] duplicates name %q", id, index, name)
	}
	seen[name] = true
	if !validName.MatchString(strings.TrimSpace(s.Source)) {
		return fmt.Errorf("tool manifest %q build stage %s has invalid source %q", id, name, s.Source)
	}
	switch strings.TrimSpace(s.Prefix) {
	case "", buildPrefixPayload, buildPrefixDeps:
	default:
		return fmt.Errorf("tool manifest %q build stage %s has unsupported prefix %q", id, name, s.Prefix)
	}
	placeholders := buildStagePlaceholders("/prefix", "/deps", 1)
	for _, group := range [][]string{s.Configure, s.Make, s.Install} {
		for _, value := range group {
			if _, err := renderDownloadTemplate("build stage "+name, value, placeholders); err != nil {
				return fmt.Errorf("tool manifest %q %w", id, err)
			}
		}
	}
	for key, value := range s.Env {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("tool manifest %q build stage %s has invalid env name %q", id, name, key)
		}
		if _, err := renderDownloadTemplate("build stage "+name, value, placeholders); err != nil {
			return fmt.Errorf("tool manifest %q %w", id, err)
		}
	}

	return nil
}

// buildStagePlaceholders returns the template values stage fields may use.
func buildStagePlaceholders(prefix, deps string, jobs int) map[string]string {
	return map[string]string{
		"prefix": prefix,
		"deps":   deps,
		"jobs":   fmt.Sprintf("%d", jobs),
	}
}

// loadBuildFrom replaces a build.from reference with the referenced manifest's
// recipe plus this manifest's extra configure arguments. php-zts uses it to
// share php's recipe and add --enable-zts, so the two cannot drift apart.
func loadBuildFrom(fsys fs.FS, tool string, build manifestBuild) (manifestBuild, error) {
	from := strings.ToLower(strings.TrimSpace(build.From))
	if from == "" {
		return build, nil
	}
	data, err := fs.ReadFile(fsys, path.Clean(from+".yaml"))
	if err != nil {
		return manifestBuild{}, fmt.Errorf("tool manifest %q build.from: %w", tool, err)
	}
	referenced, err := parsePluginManifest(data)
	if err != nil {
		return manifestBuild{}, fmt.Errorf("tool manifest %q build.from: %w", tool, err)
	}
	if referenced.Build.isZero() || strings.TrimSpace(referenced.Build.From) != "" {
		return manifestBuild{}, fmt.Errorf("tool manifest %q build.from %q must reference a manifest with its own recipe", tool, from)
	}

	merged := referenced.Build
	merged.Stages = append([]manifestBuildStage(nil), referenced.Build.Stages...)
	if len(build.ExtraConfigure) > 0 {
		index := merged.configureStageIndex()
		if index < 0 {
			return manifestBuild{}, fmt.Errorf("tool manifest %q build.extra-configure needs a configure stage in %q", tool, from)
		}
		stage := merged.Stages[index]
		stage.Configure = append(append([]string(nil), stage.Configure...), build.ExtraConfigure...)
		merged.Stages[index] = stage
	}
	if err := merged.validateRecipe(tool); err != nil {
		return manifestBuild{}, err
	}

	return merged, nil
}

// buildPackagePath converts a validated manifest path to a host path under root.
func buildPackagePath(root, relative string) string {
	return filepath.Join(root, filepath.FromSlash(strings.TrimSpace(relative)))
}
