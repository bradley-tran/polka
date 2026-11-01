package plugins

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// FrameworkDetection describes package-name and file signals for a framework.
// Prefix matches precede substring matches, which precede marker files.
type FrameworkDetection struct {
	PackagePrefixes   []string `yaml:"package-prefixes"`
	PackageSubstrings []string `yaml:"package-substrings"`
	Markers           []string `yaml:"markers"`
	Priority          int      `yaml:"priority"`
}

func (d FrameworkDetection) clone() FrameworkDetection {
	d.PackagePrefixes = slices.Clone(d.PackagePrefixes)
	d.PackageSubstrings = slices.Clone(d.PackageSubstrings)
	d.Markers = slices.Clone(d.Markers)
	return d
}

// validate keeps detection signals nonempty, distinctive, and inside the candidate root.
func (d FrameworkDetection) validate() error {
	for _, pattern := range append(slices.Clone(d.PackagePrefixes), d.PackageSubstrings...) {
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("detection package patterns cannot be empty")
		}
	}
	for _, marker := range d.Markers {
		if marker == "composer.json" {
			return fmt.Errorf("detection marker composer.json is not distinctive")
		}
		if err := validateFrameworkManifestRelativePath(marker, "detection.markers", false); err != nil {
			return err
		}
	}
	return nil
}

// ComposerCreateProjectDirectory parses the Composer target relative to the
// working directory. args must include the create-project verb.
func ComposerCreateProjectDirectory(args []string, workingDir string) string {
	return composerCreateProjectDirectory(args, workingDir)
}

// DetectFramework evaluates only the active registry. Missing markers are
// ordinary non-matches; other filesystem failures are returned to the caller.
func (r *Registry) DetectFramework(pkg, dir string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(pkg))
	if colon := strings.Index(name, ":"); colon >= 0 {
		name = name[:colon]
	}
	frameworks := r.Frameworks()
	slices.SortFunc(frameworks, func(a, b FrameworkPlugin) int {
		ap, bp := a.Detection().Priority, b.Detection().Priority
		if ap > bp {
			return -1
		}
		if ap < bp {
			return 1
		}
		return strings.Compare(a.ID(), b.ID())
	})
	for _, category := range []string{"prefix", "substring", "marker"} {
		for _, framework := range frameworks {
			d := framework.Detection()
			switch category {
			case "prefix":
				for _, pattern := range d.PackagePrefixes {
					if strings.HasPrefix(name, strings.ToLower(pattern)) {
						return framework.ID(), nil
					}
				}
			case "substring":
				for _, pattern := range d.PackageSubstrings {
					if strings.Contains(name, strings.ToLower(pattern)) {
						return framework.ID(), nil
					}
				}
			case "marker":
				found, err := frameworkMarkersExist(dir, d.Markers)
				if err != nil {
					return "", fmt.Errorf("detect framework %q: %w", framework.ID(), err)
				}
				if found {
					return framework.ID(), nil
				}
			}
		}
	}
	return "", nil
}

// frameworkMarkersExist checks regular files, never directories with a marker
// name. It is also used with the more permissive application-root hints.
func frameworkMarkersExist(dir string, markers []string) (bool, error) {
	for _, marker := range markers {
		filename := filepath.Join(dir, filepath.FromSlash(marker))
		info, err := os.Stat(filename)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("stat marker %s: %w", filename, err)
		}
		if info.Mode().IsRegular() {
			return true, nil
		}
	}
	return false, nil
}

// frameworkComposerAppRoot keeps Composer target and docroot precedence while
// consulting manifest hints rather than testing framework IDs.
func frameworkComposerAppRoot(ctx PostComposerContext, root frameworkAppRootConfig) (string, error) {
	candidates := make([]string, 0, 4)
	if target := composerCreateProjectDirectory(ctx.Args, ctx.WorkingDir); target != "" {
		candidates = append(candidates, target)
	}
	if docroot := frameworkAppRootFromDocroot(ctx.ProjectDir, ctx.Environment.Docroot, root.PublicDir); docroot != "" {
		candidates = append(candidates, docroot)
	}
	if dir := cleanAbsolutePath(ctx.WorkingDir); dir != "" {
		candidates = append(candidates, dir)
	}
	if dir := cleanAbsolutePath(ctx.ProjectDir); dir != "" {
		candidates = append(candidates, dir)
	}
	for _, candidate := range dedupePaths(candidates) {
		found, err := frameworkMarkersExist(candidate, root.Markers)
		if err != nil {
			return "", err
		}
		if found {
			return candidate, nil
		}
	}
	if len(candidates) > 0 {
		return candidates[0], nil
	}
	return ".", nil
}
