package plugins

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// frameworkManifestPostComposer resolves each action in memory before writing
// its target. An unsupported PHP shape never causes that target to be replaced.
// Actions come from the manifest or from a script hook, which only compiles
// once the database guard has passed.
func frameworkManifestPostComposer(ctx PostComposerContext, plugin builtinFrameworkPlugin) error {
	m := plugin.manifest
	scripted := m.Script.hasHook(frameworkHookPostComposer)
	if len(m.PostComposer.Actions) == 0 && !scripted {
		return nil
	}
	state := newFrameworkValueState(m, RuntimeEnvContext{Environment: ctx.Environment, Database: ctx.Database})
	if m.PostComposer.RequiresDatabase && state.database == nil {
		return nil
	}
	root, err := frameworkComposerAppRoot(ctx, m.AppRoot)
	if err != nil {
		return fmt.Errorf("framework %q application root: %w", m.ID, err)
	}
	actions := m.PostComposer.Actions
	if scripted {
		actions, err = frameworkScriptActions(ctx, plugin, state.database, root)
		if err != nil {
			return err
		}
	}
	for i, action := range actions {
		base := root
		if action.Base == "docroot" {
			base = frameworkDocrootPath(ctx, root, m.AppRoot.PublicDir)
		}
		if err := runFrameworkAction(action, base, state, plugin); err != nil {
			return fmt.Errorf("framework %q action %d (%s, %s): %w", m.ID, i, action.Type, action.Path, err)
		}
	}
	return nil
}

// frameworkContainedPath checks existing symlink ancestors as well as lexical
// containment. Configured application and docroot bases may themselves be links.
func frameworkContainedPath(base, relative string) (string, error) {
	if err := validateFrameworkManifestRelativePath(relative, "action path", false); err != nil {
		return "", err
	}
	base, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	target := filepath.Join(base, filepath.FromSlash(relative))
	resolvedBase, err := frameworkExistingPath(base)
	if err != nil {
		return "", err
	}
	resolvedTarget, err := frameworkExistingPath(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(resolvedBase, resolvedTarget)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("action path escapes its base")
	}
	// Write through contained symlinks instead of replacing the link itself.
	return resolvedTarget, nil
}

// frameworkExistingPath resolves links in the nearest existing ancestor of a
// possibly missing output path, propagating failures other than absence.
func frameworkExistingPath(filename string) (string, error) {
	resolved, err := filepath.EvalSymlinks(filename)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(filename)
	if parent == filename {
		return "", err
	}
	resolved, err = frameworkExistingPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(filename)), nil
}

// readFrameworkActionFile prioritizes an existing application file, then an
// application fallback, then an embedded fallback. Only absence is recoverable.
func readFrameworkActionFile(a frameworkActionManifest, base, target string, plugin builtinFrameworkPlugin) (string, error) {
	data, err := os.ReadFile(target)
	if err == nil {
		return string(data), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if a.Fallback != "" {
		filename, err := frameworkContainedPath(base, a.Fallback)
		if err != nil {
			return "", err
		}
		data, err = os.ReadFile(filename)
		if err == nil {
			return string(data), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	if a.FallbackTemplateFile != "" {
		return frameworkTemplate(plugin.files, a.FallbackTemplateFile)
	}
	if a.Type == "php-include" {
		return "<?php\n", nil
	}
	if a.Type != "dotenv" {
		return "", fmt.Errorf("missing PHP file and no fallback-template-file")
	}
	return "", nil
}

// runFrameworkAction resolves all values and edits one complete target before committing it.
func runFrameworkAction(a frameworkActionManifest, base string, state *frameworkValueState, plugin builtinFrameworkPlugin) error {
	target, err := frameworkContainedPath(base, a.Path)
	if err != nil {
		return err
	}
	keys := orderedFrameworkKeys(a.Values, a.Order)
	values := make(map[string]any, len(keys))
	for _, key := range keys {
		value, err := state.eval(a.Values[key])
		if err != nil {
			return fmt.Errorf("resolve value %q: %w", key, err)
		}
		values[key] = value
	}
	var contents string
	if a.Type == "write-file" {
		contents, err = frameworkTemplate(plugin.files, a.TemplateFile)
	} else {
		contents, err = readFrameworkActionFile(a, base, target, plugin)
	}
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	switch a.Type {
	case "dotenv":
		assignments := make([]dotenvAssignment, 0, len(keys))
		for _, key := range keys {
			assignments = append(assignments, dotenvAssignment{Name: key, Value: frameworkScalarText(values[key])})
		}
		eol := phpEOL(contents)
		contents = upsertDotenvAssignments(contents, assignments)
		if eol == "\r\n" {
			contents = strings.ReplaceAll(contents, "\n", eol)
		}
	case "php-array":
		for _, update := range a.Updates {
			for _, key := range orderedFrameworkKeys(update.Values, update.Order) {
				value, err := state.eval(update.Values[key])
				if err != nil {
					return fmt.Errorf("resolve array value %q: %w", key, err)
				}
				literal, err := renderPHPScalar(value)
				if err != nil {
					return err
				}
				contents, err = patchPHPArrayValue(contents, append(slices.Clone(update.Path), key), literal)
				if err != nil {
					return err
				}
			}
		}
	case "php-constants":
		literals := map[string]string{}
		for _, key := range keys {
			literal, err := renderPHPScalar(values[key])
			if err != nil {
				return err
			}
			literals[key] = literal
		}
		contents, err = patchPHPConstants(contents, literals, keys, a.InsertBefore)
	case "write-file":
		bindings := map[string]string{}
		for _, key := range keys {
			if a.Encoding == "php-literal" {
				literal, err := renderPHPScalar(values[key])
				if err != nil {
					return err
				}
				bindings[key] = literal
			} else {
				bindings[key] = frameworkScalarText(values[key])
			}
		}
		// Replace once against the template, so placeholder-looking credentials
		// can never trigger a second substitution.
		contents = frameworkPlaceholder.ReplaceAllStringFunc(contents, func(match string) string { return bindings[match[2:len(match)-2]] })
	case "php-include":
		mode = 0o644
		contents, err = patchPHPInclude(contents, a.Include, a.Marker)
	}
	if err != nil {
		return err
	}
	return writeFrameworkFile(target, []byte(contents), mode)
}

// writeFrameworkFile stages complete output beside its destination. A failed
// write or rename leaves an existing config intact and cleans up the temporary.
func writeFrameworkFile(target string, data []byte, mode os.FileMode) (result error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".polka-config-*")
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			result = errors.Join(result, file.Close())
		}
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	closed = true
	if err := file.Close(); err != nil {
		return err
	}
	return replaceFrameworkFile(file.Name(), target)
}

// replaceFrameworkFile temporarily clears the Windows read-only attribute,
// which otherwise prevents replacing files such as Drupal's settings.php.
// Restore protection on the replacement, or on the original if rename fails.
func replaceFrameworkFile(staged, target string) (result error) {
	if runtime.GOOS != "windows" {
		return os.Rename(staged, target)
	}
	info, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return os.Rename(staged, target)
	}
	if err != nil {
		return fmt.Errorf("stat framework config before replacement: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o200 != 0 {
		return os.Rename(staged, target)
	}
	if err := os.Chmod(target, info.Mode().Perm()|0o200); err != nil {
		return fmt.Errorf("make framework config writable for replacement: %w", err)
	}
	defer func() {
		if err := os.Chmod(target, info.Mode().Perm()); err != nil {
			result = errors.Join(result, fmt.Errorf("restore framework config read-only attribute: %w", err))
		}
	}()
	return os.Rename(staged, target)
}
