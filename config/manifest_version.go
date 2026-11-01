package config

import (
	"fmt"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

// ManifestVersion identifies the project YAML schema, independently of the
// integer schema versions used by caches and runtime state.
type ManifestVersion string

// CurrentManifestVersion is the only supported project manifest schema.
const CurrentManifestVersion ManifestVersion = "0.2"

// UnmarshalYAML preserves a version's scalar spelling instead of converting it
// through a floating-point number. Both quoted and plain scalars are accepted.
func (v *ManifestVersion) UnmarshalYAML(node ast.Node) error {
	switch scalar := node.(type) {
	case *ast.StringNode:
		*v = ManifestVersion(scalar.Value)
	case *ast.FloatNode, *ast.IntegerNode:
		*v = ManifestVersion(node.GetToken().Value)
	default:
		return fmt.Errorf("project manifest version must be a string or numeric scalar")
	}
	return nil
}

// MarshalYAML writes the canonical plain scalar used in generated manifests.
func (v ManifestVersion) MarshalYAML() ([]byte, error) {
	if v != CurrentManifestVersion {
		return nil, fmt.Errorf("unsupported project manifest version %q", v)
	}
	return []byte(v), nil
}

// ValidateProjectManifestVersion checks the header before any environment
// decoding, so obsolete schemas cannot be coerced into current config types.
func ValidateProjectManifestVersion(data []byte, path string) error {
	var header struct {
		Version ManifestVersion `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return fmt.Errorf("decode project manifest %s: %w; delete %s and run polka init (or polka init <framework>) to regenerate it", path, err, path)
	}
	if header.Version != CurrentManifestVersion {
		return fmt.Errorf("unsupported project manifest version %q in %s; expected %s; delete %s and run polka init (or polka init <framework>) to regenerate it", header.Version, path, CurrentManifestVersion, path)
	}
	return nil
}
