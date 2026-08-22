package plugins

import (
	"embed"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"

	"polka/config"
)

//go:embed manifests/presets/*.yaml
var builtinProjectPresetManifestFiles embed.FS

//go:embed manifests/presets/templates/*
var builtinProjectPresetTemplateFiles embed.FS

type projectPresetManifest struct {
	ID          string                          `yaml:"id"`
	Description string                          `yaml:"description"`
	Defaults    config.EnvironmentFile          `yaml:"defaults"`
	Scaffold    []projectPresetScaffoldManifest `yaml:"scaffold"`
}

type projectPresetScaffoldManifest struct {
	Path         string `yaml:"path"`
	TemplateFile string `yaml:"template-file"`
}

func newManifestProjectPreset(name string) ProjectPreset {
	manifest, err := loadBuiltinProjectPresetManifest(name)
	if err != nil {
		panic(err)
	}
	preset, err := manifest.toPreset()
	if err != nil {
		panic(err)
	}

	return preset
}

func loadBuiltinProjectPresetManifest(name string) (projectPresetManifest, error) {
	manifestPath := "manifests/presets/" + strings.TrimSpace(name) + ".yaml"
	data, err := builtinProjectPresetManifestFiles.ReadFile(manifestPath)
	if err != nil {
		return projectPresetManifest{}, fmt.Errorf("read builtin project preset manifest %s: %w", manifestPath, err)
	}

	return parseProjectPresetManifest(data)
}

func parseProjectPresetManifest(data []byte) (projectPresetManifest, error) {
	var manifest projectPresetManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return projectPresetManifest{}, fmt.Errorf("parse project preset manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
		return projectPresetManifest{}, err
	}

	manifest.ID = strings.ToLower(strings.TrimSpace(manifest.ID))
	manifest.Description = strings.TrimSpace(manifest.Description)
	for index := range manifest.Scaffold {
		manifest.Scaffold[index].Path = path.Clean(strings.TrimSpace(manifest.Scaffold[index].Path))
		manifest.Scaffold[index].TemplateFile = strings.TrimSpace(manifest.Scaffold[index].TemplateFile)
	}

	return manifest, nil
}

func (m projectPresetManifest) validate() error {
	id := strings.ToLower(strings.TrimSpace(m.ID))
	if id == "" {
		return fmt.Errorf("project preset manifest id cannot be empty")
	}
	if !validPluginID.MatchString(id) {
		return fmt.Errorf("invalid project preset manifest id %q: use letters, numbers, dots, dashes, or underscores", m.ID)
	}
	if strings.TrimSpace(m.Defaults.Framework) != "" {
		return fmt.Errorf("project preset manifest %q defaults.framework must be empty", id)
	}
	if err := config.ValidateWorkersConfig(m.Defaults.Workers); err != nil {
		return fmt.Errorf("project preset manifest %q: %w", id, err)
	}

	for _, scaffold := range m.Scaffold {
		if err := validateProjectPresetScaffoldPath(scaffold.Path); err != nil {
			return fmt.Errorf("project preset manifest %q: %w", id, err)
		}
		templateFile := strings.TrimSpace(scaffold.TemplateFile)
		if templateFile == "" {
			return fmt.Errorf("project preset manifest %q scaffold template-file cannot be empty", id)
		}
		if strings.ContainsAny(templateFile, `/\\`) || templateFile == "." || templateFile == ".." {
			return fmt.Errorf("project preset manifest %q scaffold template-file %q must be a filename", id, scaffold.TemplateFile)
		}
		templatePath := "manifests/presets/templates/" + templateFile
		if _, err := builtinProjectPresetTemplateFiles.ReadFile(templatePath); err != nil {
			return fmt.Errorf("project preset manifest %q read scaffold template %s: %w", id, templatePath, err)
		}
	}

	return nil
}

func validateProjectPresetScaffoldPath(target string) error {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return fmt.Errorf("scaffold path cannot be empty")
	}
	if strings.Contains(trimmed, `\`) || strings.HasPrefix(trimmed, "/") || filepath.IsAbs(trimmed) || filepath.VolumeName(trimmed) != "" {
		return fmt.Errorf("scaffold path %q must be slash-separated and relative", target)
	}
	segments := strings.Split(trimmed, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("scaffold path %q must not contain empty, . or .. segments", target)
		}
	}
	lower := strings.ToLower(trimmed)
	if lower == "polka.yaml" || (strings.HasPrefix(lower, "polka.") && strings.HasSuffix(lower, ".yaml")) || lower == ".polka" || strings.HasPrefix(lower, ".polka/") {
		return fmt.Errorf("scaffold path %q is reserved for init", target)
	}

	return nil
}

func (m projectPresetManifest) toPreset() (ProjectPreset, error) {
	defaults := config.NormalizeEnvironment("", config.EnvironmentFileToEnvironment("", m.Defaults))
	scaffold := make([]projectPresetScaffold, 0, len(m.Scaffold))
	for _, item := range m.Scaffold {
		templatePath := "manifests/presets/templates/" + item.TemplateFile
		content, err := builtinProjectPresetTemplateFiles.ReadFile(templatePath)
		if err != nil {
			return nil, fmt.Errorf("read project preset scaffold template %s: %w", templatePath, err)
		}
		scaffold = append(scaffold, projectPresetScaffold{path: item.Path, template: content})
	}

	return builtinProjectPreset{
		id:          m.ID,
		description: m.Description,
		defaults:    defaults,
		scaffold:    scaffold,
	}, nil
}
