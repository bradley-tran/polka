package plugins

import (
	"path/filepath"
	"regexp"
	"strings"

	"polka/config"
)

const (
	// PHPExtension is the built-in native PHP extension project preset.
	PHPExtension = "php-extension"
)

var (
	invalidExtensionNameCharacter = regexp.MustCompile(`[^a-z0-9_]+`)
	repeatedExtensionUnderscore   = regexp.MustCompile(`_+`)
	invalidPackageNameCharacter   = regexp.MustCompile(`[^a-z0-9]+`)
	repeatedPackageDash           = regexp.MustCompile(`-+`)
)

// ProjectPreset is a starting environment config for a project shape that is
// not a framework. Presets contribute only init-time defaults and files.
type ProjectPreset interface {
	ID() string
	Description() string
	Defaults() config.Environment
	ScaffoldFiles(ScaffoldContext) []ScaffoldFile
}

// ScaffoldFile is one project-relative file rendered by a project preset.
type ScaffoldFile struct {
	Path    string
	Content []byte
}

// ScaffoldContext carries values that preset templates interpolate.
type ScaffoldContext struct {
	ProjectDir  string
	PackageName string
	PHPVersion  string
}

type builtinProjectPreset struct {
	id          string
	description string
	defaults    config.Environment
	scaffold    []projectPresetScaffold
}

type projectPresetScaffold struct {
	path     string
	template []byte
}

// DefaultProjectPresets returns the built-in project presets.
func DefaultProjectPresets() []ProjectPreset {
	return []ProjectPreset{newManifestProjectPreset(PHPExtension)}
}

func (p builtinProjectPreset) ID() string {
	return p.id
}

func (p builtinProjectPreset) Description() string {
	return p.description
}

func (p builtinProjectPreset) Defaults() config.Environment {
	return config.NormalizeEnvironment("", p.defaults)
}

// ScaffoldFiles renders the preset's embedded templates using literal
// placeholder substitution.
func (p builtinProjectPreset) ScaffoldFiles(ctx ScaffoldContext) []ScaffoldFile {
	extensionName, packageName := derivePresetNames(ctx.ProjectDir)
	if explicit := strings.ToLower(strings.TrimSpace(ctx.PackageName)); explicit != "" {
		packageName = explicit
	}
	phpConstraint := "*"
	if version := strings.TrimSpace(ctx.PHPVersion); version != "" {
		phpConstraint = "^" + version
	}

	files := make([]ScaffoldFile, 0, len(p.scaffold))
	for _, scaffold := range p.scaffold {
		content := string(scaffold.template)
		content = strings.ReplaceAll(content, "{{package}}", packageName)
		content = strings.ReplaceAll(content, "{{extension}}", extensionName)
		content = strings.ReplaceAll(content, "{{php-constraint}}", phpConstraint)
		files = append(files, ScaffoldFile{Path: scaffold.path, Content: []byte(content)})
	}

	return files
}

// derivePresetNames turns a project directory basename into valid Composer
// package and PHP extension identifiers.
func derivePresetNames(projectDir string) (string, string) {
	base := strings.ToLower(strings.TrimSpace(filepath.Base(filepath.Clean(projectDir))))

	extensionName := invalidExtensionNameCharacter.ReplaceAllString(base, "_")
	extensionName = repeatedExtensionUnderscore.ReplaceAllString(extensionName, "_")
	extensionName = strings.Trim(extensionName, "_")
	if extensionName == "" || (extensionName[0] >= '0' && extensionName[0] <= '9') {
		extensionName = "my_extension"
	}

	packageSuffix := invalidPackageNameCharacter.ReplaceAllString(base, "-")
	packageSuffix = repeatedPackageDash.ReplaceAllString(packageSuffix, "-")
	packageSuffix = strings.Trim(packageSuffix, "-")
	if packageSuffix == "" {
		packageSuffix = "my-extension"
	}

	return extensionName, "vendor/" + packageSuffix
}
