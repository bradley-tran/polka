package tools

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"go.starlark.net/starlark"

	"polka/script"
)

// buildSource is one validated source archive from a build_sources hook.
type buildSource struct {
	Name              string
	URL               string
	FileName          string
	ArchiveFormat     archiveFormat
	ChecksumAlgorithm checksumAlgorithm
	Checksum          string
	ChecksumURL       string
	// Unverified marks a source whose upstream publishes no checksum. It must
	// be declared explicitly, so a recipe cannot skip verification by omission.
	Unverified bool
}

// buildSourcePlan is the validated result of a build_sources hook.
type buildSourcePlan struct {
	Version string
	Sources map[string]buildSource
}

// resolveBuildSources runs the manifest's build_sources hook and validates the
// plan against the recipe. Downloading and verifying stay in Go.
func resolveBuildSources(client *http.Client, m pluginManifest, requestedVersion, goos, goarch string) (buildSourcePlan, error) {
	tool := strings.ToLower(strings.TrimSpace(m.ID))
	requestedVersion = strings.TrimSpace(requestedVersion)
	if requestedVersion == "" {
		return buildSourcePlan{}, fmt.Errorf("%s version cannot be empty", tool)
	}
	module, err := m.scriptModule()
	if err != nil {
		return buildSourcePlan{}, err
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
	result, err := module.Call(scriptHookBuildSources, locals, ctx)
	if err != nil {
		return buildSourcePlan{}, scriptHookError(tool, m.Script.File, err)
	}
	plan, err := parseBuildSourcePlan(result)
	if err != nil {
		return buildSourcePlan{}, fmt.Errorf("%s script %s %s: %w", tool, m.Script.File, scriptHookBuildSources, err)
	}

	// The resolved version is recorded in cache metadata and decides what gets
	// compiled, so it passes the same checks as a downloaded version.
	if err := validateVersion(tool, plan.Version); err != nil {
		return buildSourcePlan{}, err
	}
	if !m.VersionConstraint.allowsResolved(plan.Version) {
		return buildSourcePlan{}, m.VersionConstraint.error(tool, plan.Version)
	}
	if minVersion := strings.TrimSpace(m.Build.MinVersion); minVersion != "" && compareVersions(plan.Version, minVersion) < 0 {
		return buildSourcePlan{}, fmt.Errorf("building %s %s is not supported: the recipe requires %s or newer", tool, plan.Version, minVersion)
	}
	for _, stage := range m.Build.Stages {
		if _, ok := plan.Sources[strings.TrimSpace(stage.Source)]; !ok {
			return buildSourcePlan{}, fmt.Errorf("%s script %s %s returned no source %q for build stage %s", tool, m.Script.File, scriptHookBuildSources, stage.Source, stage.Name)
		}
	}

	return plan, nil
}

// parseBuildSourcePlan converts a hook result dict into a plan, rejecting
// unknown keys and values so a typo cannot silently drop a checksum.
func parseBuildSourcePlan(result starlark.Value) (buildSourcePlan, error) {
	dict, ok := result.(*starlark.Dict)
	if !ok {
		return buildSourcePlan{}, fmt.Errorf("result must be a dict, got %s", result.Type())
	}

	plan := buildSourcePlan{Sources: map[string]buildSource{}}
	for _, item := range dict.Items() {
		key, ok := starlark.AsString(item[0])
		if !ok {
			return buildSourcePlan{}, fmt.Errorf("result keys must be strings, got %s", item[0].Type())
		}
		switch key {
		case "version":
			text, ok := starlark.AsString(item[1])
			if !ok {
				return buildSourcePlan{}, fmt.Errorf("result version must be a string, got %s", item[1].Type())
			}
			plan.Version = strings.TrimSpace(text)
		case "sources":
			sources, ok := item[1].(*starlark.Dict)
			if !ok {
				return buildSourcePlan{}, fmt.Errorf("result sources must be a dict, got %s", item[1].Type())
			}
			for _, sourceItem := range sources.Items() {
				name, ok := starlark.AsString(sourceItem[0])
				if !ok || !validName.MatchString(name) {
					return buildSourcePlan{}, fmt.Errorf("result has invalid source name %s", sourceItem[0].String())
				}
				source, err := parseBuildSource(name, sourceItem[1])
				if err != nil {
					return buildSourcePlan{}, err
				}
				plan.Sources[name] = source
			}
		default:
			return buildSourcePlan{}, fmt.Errorf("result has unsupported key %q", key)
		}
	}
	if plan.Version == "" {
		return buildSourcePlan{}, fmt.Errorf("result requires version")
	}
	if len(plan.Sources) == 0 {
		return buildSourcePlan{}, fmt.Errorf("result requires sources")
	}

	return plan, nil
}

// parseBuildSource validates one source entry. Its fields become download
// requests and staging paths, and may come from upstream metadata.
func parseBuildSource(name string, value starlark.Value) (buildSource, error) {
	dict, ok := value.(*starlark.Dict)
	if !ok {
		return buildSource{}, fmt.Errorf("source %s must be a dict, got %s", name, value.Type())
	}

	source := buildSource{Name: name}
	var algorithm string
	stringFields := map[string]*string{
		"url":                &source.URL,
		"filename":           &source.FileName,
		"checksum":           &source.Checksum,
		"checksum_url":       &source.ChecksumURL,
		"checksum_algorithm": &algorithm,
	}
	for _, item := range dict.Items() {
		key, ok := starlark.AsString(item[0])
		if !ok {
			return buildSource{}, fmt.Errorf("source %s keys must be strings, got %s", name, item[0].Type())
		}
		if key == "unverified" {
			flag, ok := item[1].(starlark.Bool)
			if !ok {
				return buildSource{}, fmt.Errorf("source %s unverified must be a bool, got %s", name, item[1].Type())
			}
			source.Unverified = bool(flag)
			continue
		}
		target, ok := stringFields[key]
		if !ok {
			return buildSource{}, fmt.Errorf("source %s has unsupported key %q", name, key)
		}
		if item[1] == starlark.None {
			continue
		}
		text, ok := starlark.AsString(item[1])
		if !ok {
			return buildSource{}, fmt.Errorf("source %s %s must be a string, got %s", name, key, item[1].Type())
		}
		*target = strings.TrimSpace(text)
	}

	if err := validateScriptURL(source.URL); err != nil {
		return buildSource{}, fmt.Errorf("source %s url: %w", name, err)
	}
	if source.FileName == "" || source.FileName == ".." || strings.ContainsAny(source.FileName, `/\`) {
		return buildSource{}, fmt.Errorf("source %s has invalid filename %q", name, source.FileName)
	}
	format, err := sourceArchiveFormat(source.FileName)
	if err != nil {
		return buildSource{}, fmt.Errorf("source %s: %w", name, err)
	}
	source.ArchiveFormat = format

	if source.Unverified {
		if source.Checksum != "" || source.ChecksumURL != "" || algorithm != "" {
			return buildSource{}, fmt.Errorf("source %s cannot be unverified and declare a checksum", name)
		}
		return source, nil
	}
	if source.Checksum == "" && source.ChecksumURL == "" {
		return buildSource{}, fmt.Errorf("source %s requires checksum or checksum_url, or an explicit unverified=True", name)
	}
	switch parsed := checksumAlgorithm(strings.ToLower(algorithm)); parsed {
	case checksumAlgorithmMD5, checksumAlgorithmSHA256, checksumAlgorithmSHA3_256:
		source.ChecksumAlgorithm = parsed
	default:
		return buildSource{}, fmt.Errorf("source %s has unsupported checksum_algorithm %q", name, algorithm)
	}
	if source.ChecksumURL != "" {
		if err := validateScriptURL(source.ChecksumURL); err != nil {
			return buildSource{}, fmt.Errorf("source %s checksum_url: %w", name, err)
		}
	}
	if source.Checksum != "" {
		checksum, _, err := validateChecksumValue(source.ChecksumAlgorithm, source.Checksum)
		if err != nil {
			return buildSource{}, fmt.Errorf("source %s checksum: %w", name, err)
		}
		source.Checksum = checksum
	}

	return source, nil
}

// sourceArchiveFormat infers a source archive's format from its file name.
// Only formats whose extraction can preserve modification times are accepted.
func sourceArchiveFormat(fileName string) (archiveFormat, error) {
	lower := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return archiveFormatTarGz, nil
	case strings.HasSuffix(lower, ".tar.xz"):
		return archiveFormatTarXz, nil
	default:
		return "", fmt.Errorf("unsupported source archive %q: use tar.gz or tar.xz", fileName)
	}
}

// sortedBuildSources returns the plan's sources in a stable order, so downloads
// and recorded metadata do not vary between runs.
func sortedBuildSources(plan buildSourcePlan) []buildSource {
	names := make([]string, 0, len(plan.Sources))
	for name := range plan.Sources {
		names = append(names, name)
	}
	sort.Strings(names)

	sources := make([]buildSource, 0, len(names))
	for _, name := range names {
		sources = append(sources, plan.Sources[name])
	}

	return sources
}
