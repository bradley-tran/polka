package tools

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"polka/config"
	"polka/script"
)

const (
	// scriptPostInstallLocal is the thread-local key marking a post_install hook
	// call, which unlocks the path and crypto modules.
	scriptPostInstallLocal = "polka.post_install"
	// scriptRandomMaxBytes caps one crypto.random_base64 request.
	scriptRandomMaxBytes = 1024
)

// scriptPathModule exposes host path helpers to post_install hooks.
var scriptPathModule = &starlarkstruct.Module{
	Name: "path",
	Members: starlark.StringDict{
		"join": starlark.NewBuiltin("path.join", scriptPathJoin),
	},
}

// scriptCryptoModule exposes secret generation to post_install hooks.
var scriptCryptoModule = &starlarkstruct.Module{
	Name: "crypto",
	Members: starlark.StringDict{
		"random_base64": starlark.NewBuiltin("crypto.random_base64", scriptCryptoRandomBase64),
	},
}

// requirePostInstallHook rejects a builtin call outside a post_install hook.
func requirePostInstallHook(thread *starlark.Thread, b *starlark.Builtin) error {
	if thread.Local(scriptPostInstallLocal) == nil {
		return fmt.Errorf("%s: only available in the post_install hook", b.Name())
	}

	return nil
}

// scriptPathJoin implements path.join(*parts) using host path semantics, for
// building absolute paths that end up in generated configuration.
func scriptPathJoin(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := requirePostInstallHook(thread, b); err != nil {
		return nil, err
	}
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("%s: unexpected keyword arguments", b.Name())
	}
	parts := make([]string, 0, len(args))
	for index, arg := range args {
		part, ok := starlark.AsString(arg)
		if !ok {
			return nil, fmt.Errorf("%s: argument %d must be a string, got %s", b.Name(), index, arg.Type())
		}
		parts = append(parts, part)
	}

	return starlark.String(filepath.Join(parts...)), nil
}

// scriptCryptoRandomBase64 implements crypto.random_base64(n), returning n
// cryptographically random bytes as unpadded base64.
func scriptCryptoRandomBase64(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := requirePostInstallHook(thread, b); err != nil {
		return nil, err
	}
	var size int
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &size); err != nil {
		return nil, err
	}
	if size < 1 || size > scriptRandomMaxBytes {
		return nil, fmt.Errorf("%s: size must be between 1 and %d, got %d", b.Name(), scriptRandomMaxBytes, size)
	}
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return nil, script.Fail(fmt.Errorf("%s: %w", b.Name(), err))
	}

	return starlark.String(base64.RawStdEncoding.EncodeToString(buffer)), nil
}

// scriptPostInstallPlan is the validated result of a post_install hook. Paths
// are slash-separated and relative to the install directory.
type scriptPostInstallPlan struct {
	Mkdir []string
	Write map[string]string
}

// scriptPostInstallFunc adapts the manifest's post_install hook: the script
// reads files and returns a plan, then Go validates and applies the plan inside
// the install directory. A missing install directory has nothing to configure.
func scriptPostInstallFunc(m pluginManifest, goos, goarch string) func(InstallContext) error {
	tool := strings.ToLower(strings.TrimSpace(m.ID))

	return func(ctx InstallContext) error {
		installDir := resolvePostInstallDir(tool, ctx)
		if installDir == "" {
			return nil
		}
		root, err := os.OpenRoot(installDir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("open %s install directory: %w", tool, err)
		}
		defer root.Close()

		module, err := m.scriptModule()
		if err != nil {
			return err
		}
		hookCtx, err := newScriptPostInstallContext(m, tool, goos, goarch, ctx, root)
		if err != nil {
			return err
		}
		result, err := module.Call(scriptHookPostInstall, map[string]any{scriptPostInstallLocal: true}, hookCtx)
		if err != nil {
			return script.HookError(tool, m.Script.File, err)
		}
		plan, err := parseScriptPostInstallPlan(result)
		if err != nil {
			return fmt.Errorf("%s script %s post_install: %w", tool, m.Script.File, err)
		}
		if err := plan.apply(root); err != nil {
			return fmt.Errorf("%s script %s post_install: %w", tool, m.Script.File, err)
		}

		return nil
	}
}

// newScriptPostInstallContext builds the read-only ctx value passed to
// post_install. It exposes the database engine and port but never credentials.
func newScriptPostInstallContext(m pluginManifest, tool, goos, goarch string, ctx InstallContext, root *os.Root) (starlark.Value, error) {
	rootDir, err := filepath.Abs(ctx.RootDir)
	if err != nil {
		return nil, fmt.Errorf("resolve %s root directory: %w", tool, err)
	}

	readText := starlark.NewBuiltin("ctx.read_text", func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var rawPath string
		if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &rawPath); err != nil {
			return nil, err
		}
		if err := validateInstallRelativePath(rawPath); err != nil {
			return nil, fmt.Errorf("%s: %w", b.Name(), err)
		}

		return script.ReadRootText(root, b.Name(), rawPath)
	})

	return script.NewStruct("ctx", starlark.StringDict{
		"tool":        starlark.String(tool),
		"version":     starlark.String(strings.TrimSpace(ctx.Result.Version)),
		"os":          starlark.String(goos),
		"arch":        starlark.String(goarch),
		"platform":    starlark.String(platformKey(goos, goarch)),
		"vars":        script.StringMap(m.Script.Vars),
		"environment": starlark.String(strings.TrimSpace(ctx.Environment.Name)),
		"root_dir":    starlark.String(rootDir),
		"database":    scriptDatabaseValue(ctx.Environment.Database),
		"read_text":   readText,
	}), nil
}

// scriptDatabaseValue exposes the fields of a database config that generated
// configuration may need, or None when no database is configured.
func scriptDatabaseValue(database *config.DatabaseConfig) starlark.Value {
	if database == nil {
		return starlark.None
	}

	return script.NewStruct("database", starlark.StringDict{
		"engine": starlark.String(strings.ToLower(strings.TrimSpace(database.Engine))),
		"port":   starlark.MakeInt(database.Port),
	})
}

// parseScriptPostInstallPlan converts a post_install hook result dict into a
// plan, rejecting unknown keys, wrong types, and unsafe paths. Nothing has been
// written when this fails.
func parseScriptPostInstallPlan(result starlark.Value) (scriptPostInstallPlan, error) {
	dict, ok := result.(*starlark.Dict)
	if !ok {
		return scriptPostInstallPlan{}, fmt.Errorf("result must be a dict, got %s", result.Type())
	}

	plan := scriptPostInstallPlan{}
	for _, item := range dict.Items() {
		// A non-string key reads as "" and is rejected by the default case.
		key, _ := starlark.AsString(item[0])
		if item[1] == starlark.None && (key == "write" || key == "mkdir") {
			continue
		}
		var err error
		switch key {
		case "write":
			plan.Write, err = parseScriptPlanWrites(item[1])
		case "mkdir":
			plan.Mkdir, err = parseScriptPlanDirs(item[1])
		default:
			err = fmt.Errorf("result has unsupported key %s", item[0])
		}
		if err != nil {
			return scriptPostInstallPlan{}, err
		}
	}

	return plan, nil
}

// parseScriptPlanWrites converts the plan's path-to-content map.
func parseScriptPlanWrites(value starlark.Value) (map[string]string, error) {
	dict, ok := value.(*starlark.Dict)
	if !ok {
		return nil, fmt.Errorf("result write must be a dict, got %s", value.Type())
	}
	writes := make(map[string]string, dict.Len())
	for _, item := range dict.Items() {
		rawPath, ok := starlark.AsString(item[0])
		if !ok {
			return nil, fmt.Errorf("result write paths must be strings, got %s", item[0].Type())
		}
		if err := validateInstallRelativePath(rawPath); err != nil {
			return nil, fmt.Errorf("result write: %w", err)
		}
		content, ok := starlark.AsString(item[1])
		if !ok {
			return nil, fmt.Errorf("result write %q must be a string, got %s", rawPath, item[1].Type())
		}
		writes[rawPath] = content
	}

	return writes, nil
}

// parseScriptPlanDirs converts the plan's directory list.
func parseScriptPlanDirs(value starlark.Value) ([]string, error) {
	list, ok := value.(*starlark.List)
	if !ok {
		return nil, fmt.Errorf("result mkdir must be a list, got %s", value.Type())
	}
	dirs := make([]string, 0, list.Len())
	for index := range list.Len() {
		rawPath, ok := starlark.AsString(list.Index(index))
		if !ok {
			return nil, fmt.Errorf("result mkdir[%d] must be a string, got %s", index, list.Index(index).Type())
		}
		if err := validateInstallRelativePath(rawPath); err != nil {
			return nil, fmt.Errorf("result mkdir[%d]: %w", index, err)
		}
		dirs = append(dirs, rawPath)
	}

	return dirs, nil
}

// validateInstallRelativePath requires a clean slash-separated path inside the
// install directory. Backslashes are rejected so a script behaves the same on
// every platform.
func validateInstallRelativePath(rawPath string) error {
	if !fs.ValidPath(rawPath) || rawPath == "." || strings.Contains(rawPath, `\`) {
		return fmt.Errorf("path %q must be a clean slash-separated path inside the install directory", rawPath)
	}

	return nil
}

// apply creates the plan's directories and files inside root. os.Root refuses
// anything that resolves outside it, including through symlinks, and the
// preflight surfaces those refusals before any file changes.
func (p scriptPostInstallPlan) apply(root *os.Root) error {
	writes := make([]string, 0, len(p.Write))
	for rawPath := range p.Write {
		writes = append(writes, rawPath)
	}
	sort.Strings(writes)

	for _, rawPath := range append(append([]string{}, p.Mkdir...), writes...) {
		if _, err := root.Stat(filepath.FromSlash(rawPath)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("check %q: %w", rawPath, err)
		}
	}

	for _, rawPath := range p.Mkdir {
		if err := root.MkdirAll(filepath.FromSlash(rawPath), 0o755); err != nil {
			return fmt.Errorf("create directory %q: %w", rawPath, err)
		}
	}
	for _, rawPath := range writes {
		target := filepath.FromSlash(rawPath)
		if parent := filepath.Dir(target); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return fmt.Errorf("create directory for %q: %w", rawPath, err)
			}
		}
		if err := root.WriteFile(target, []byte(p.Write[rawPath]), 0o644); err != nil {
			return fmt.Errorf("write %q: %w", rawPath, err)
		}
	}

	return nil
}
