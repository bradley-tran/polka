package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// resolvePostInstallDir determines the root installation directory for a
// post-install invocation, preferring <EnvsDir>/<tool>/<version> when present
// and falling back to the directory of TargetPath for standalone callers.
func resolvePostInstallDir(tool string, ctx InstallContext) string {
	envsDir := strings.TrimSpace(ctx.EnvsDir)
	version := strings.TrimSpace(ctx.Result.Version)
	targetPath := strings.TrimSpace(ctx.Result.TargetPath)
	if envsDir != "" && version != "" {
		candidate := filepath.Join(envsDir, tool, version)
		if targetPath == "" {
			return candidate
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	if targetPath != "" {
		return filepath.Dir(targetPath)
	}

	return ""
}

// writeManifestWrappers generates the declarative command wrapper scripts
// configured on a tool manifest next to their first existing target binary.
// Wrappers whose target binary does not exist on disk are skipped.
func writeManifestWrappers(installDir string, wrappers map[string]manifestWrapper, goos, goarch string) ([]string, error) {
	if strings.TrimSpace(installDir) == "" || len(wrappers) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(wrappers))
	for name := range wrappers {
		names = append(names, name)
	}
	sort.Strings(names)

	written := make([]string, 0, len(names))
	for _, name := range names {
		spec := wrappers[name]
		candidates := manifestCandidatePaths(installDir, spec.Target, goos, goarch)
		if len(candidates) == 0 {
			continue
		}

		targetPath, ok, err := findExistingManifestFile(candidates)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}

		targetDir := filepath.Dir(targetPath)
		if spec.SkipIfExists && wrapperCommandExists(installDir, targetDir, name, goos) {
			continue
		}

		wrapperPath := filepath.Join(targetDir, name)
		if strings.TrimSpace(goos) == "windows" && !strings.HasSuffix(strings.ToLower(wrapperPath), ".cmd") {
			wrapperPath += ".cmd"
		}
		if err := os.MkdirAll(filepath.Dir(wrapperPath), 0o755); err != nil {
			return nil, fmt.Errorf("create %s wrapper directory: %w", name, err)
		}
		contents := wrapperScript(goos, filepath.Base(targetPath), spec.Args)
		if err := os.WriteFile(wrapperPath, []byte(contents), 0o755); err != nil {
			return nil, fmt.Errorf("write %s wrapper %q: %w", name, wrapperPath, err)
		}
		written = append(written, wrapperPath)
	}

	return written, nil
}

func findExistingManifestFile(candidates []string) (string, bool, error) {
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		switch {
		case err == nil && !info.IsDir():
			return candidate, true, nil
		case err == nil:
			continue
		case os.IsNotExist(err):
			continue
		default:
			return "", false, fmt.Errorf("stat %s: %w", candidate, err)
		}
	}

	return "", false, nil
}

func wrapperCommandExists(installDir, targetDir, command, goos string) bool {
	dirs := []string{targetDir, installDir, filepath.Join(installDir, "bin")}
	names := []string{command}
	if strings.TrimSpace(goos) == "windows" {
		names = []string{command + ".cmd", command + ".exe", command + ".bat", command}
	}

	seen := map[string]bool{}
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		for _, name := range names {
			candidate := filepath.Join(dir, name)
			if seen[candidate] {
				continue
			}
			seen[candidate] = true
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return true
			}
		}
	}

	return false
}

// ensureInstalledExecutables marks the installed target, any existing dispatch
// candidates, and any generated wrappers executable on non-Windows platforms.
func ensureInstalledExecutables(paths []string) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	seen := map[string]bool{}
	for _, rawPath := range paths {
		path := strings.TrimSpace(rawPath)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true

		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat installed binary %s: %w", path, err)
		}
		if info.IsDir() {
			continue
		}
		if err := os.Chmod(path, info.Mode().Perm()|0o755); err != nil {
			return fmt.Errorf("make installed binary %s executable: %w", path, err)
		}
	}

	return nil
}
