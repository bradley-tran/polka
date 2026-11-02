package plugins

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"
	"go.starlark.net/starlark"

	"polka/config"
	"polka/script"
	"polka/tools"
)

// Hook names a framework script may implement.
const (
	frameworkHookPostComposer = "post_composer"
	frameworkHookNginxConfig  = "nginx_config"
)

const (
	// frameworkManifestDir is the directory of framework manifests in their
	// filesystem. Script paths are relative to it.
	frameworkManifestDir = "manifests"
	// frameworkScriptLibraryDir holds script code shared through load(),
	// relative to frameworkManifestDir.
	frameworkScriptLibraryDir = "scripts/lib"
	// frameworkBaseAppRoot and frameworkBaseDocroot name the directories file
	// actions and ctx.read_text resolve against.
	frameworkBaseAppRoot = "app-root"
	frameworkBaseDocroot = "docroot"
)

// frameworkScript declares a sandboxed Starlark script implementing selected
// hooks. File is relative to the manifest directory. Unlike tool scripts there
// are no vars, which exist to keep upstream hosts visible in YAML, because
// framework scripts have no network access.
type frameworkScript struct {
	File  string   `yaml:"file"`
	Hooks []string `yaml:"hooks"`
}

// frameworkScriptLoader lazily compiles a manifest's script.
type frameworkScriptLoader func() (*script.Module, error)

// isZero reports whether the manifest declares no script block.
func (s frameworkScript) isZero() bool {
	return s.File == "" && len(s.Hooks) == 0
}

// hasHook reports whether the script declares hook.
func (s frameworkScript) hasHook(hook string) bool {
	return slices.Contains(s.Hooks, hook)
}

// validateScript checks the script block against the rest of the manifest
// without compiling the script, because manifests load on the dispatch path.
func (m frameworkManifest) validateScript(files fs.FS) error {
	if m.Script.isZero() {
		return nil
	}
	if m.Script.File == "" {
		return fmt.Errorf("script requires file")
	}
	if err := validateFrameworkManifestRelativePath(m.Script.File, "script.file", false); err != nil {
		return err
	}
	if path.Ext(m.Script.File) != ".star" {
		return fmt.Errorf("script.file %q must use the .star extension", m.Script.File)
	}
	if _, err := fs.Stat(files, path.Join(frameworkManifestDir, m.Script.File)); err != nil {
		return fmt.Errorf("script.file: %w", err)
	}
	if len(m.Script.Hooks) == 0 {
		return fmt.Errorf("script requires at least one hook")
	}
	seen := map[string]bool{}
	for i, hook := range m.Script.Hooks {
		if hook != frameworkHookPostComposer && hook != frameworkHookNginxConfig {
			return fmt.Errorf("script.hooks[%d] has unsupported hook %q", i, hook)
		}
		if seen[hook] {
			return fmt.Errorf("script.hooks[%d] duplicates hook %q", i, hook)
		}
		seen[hook] = true
	}
	if seen[frameworkHookPostComposer] && len(m.PostComposer.Actions) > 0 {
		return fmt.Errorf("script hook %s cannot be combined with post-composer.actions", frameworkHookPostComposer)
	}
	return nil
}

// newFrameworkScriptLoader returns a loader that compiles the manifest's script
// on first use and caches the result. Built-in plugins are constructed once per
// process, so built-in scripts also compile at most once.
func newFrameworkScriptLoader(files fs.FS, id string, spec frameworkScript) frameworkScriptLoader {
	if spec.isZero() {
		return nil
	}
	return sync.OnceValues(func() (*script.Module, error) {
		manifests, err := fs.Sub(files, frameworkManifestDir)
		if err != nil {
			return nil, err
		}
		known := []string{frameworkHookPostComposer, frameworkHookNginxConfig}
		return script.LoadHooks(manifests, spec.File, frameworkScriptOwner(id), known, spec.Hooks, script.Options{
			Predeclared: starlark.StringDict{"version": tools.ScriptVersionModule()},
			LoadDir:     frameworkScriptLibraryDir,
		})
	})
}

// frameworkScriptOwner names a framework in script errors.
func frameworkScriptOwner(id string) string {
	return fmt.Sprintf("framework %q", id)
}

// scriptModule returns the plugin's compiled script.
func (p builtinFrameworkPlugin) scriptModule() (*script.Module, error) {
	if p.loadScript == nil {
		return nil, fmt.Errorf("%s declares no script", frameworkScriptOwner(p.id))
	}
	return p.loadScript()
}

// frameworkScriptFields returns the context members every hook receives.
func frameworkScriptFields(id string, environment config.Environment) starlark.StringDict {
	return starlark.StringDict{
		"framework":   starlark.String(id),
		"environment": starlark.String(strings.TrimSpace(environment.Name)),
		"os":          starlark.String(runtime.GOOS),
		"arch":        starlark.String(runtime.GOARCH),
		"platform":    starlark.String(runtime.GOOS + "-" + runtime.GOARCH),
	}
}

// frameworkPostComposerPlan is the result of a post_composer hook. It decodes
// strictly, using the same action schema as post-composer.actions in a manifest.
type frameworkPostComposerPlan struct {
	Actions []frameworkActionManifest `yaml:"actions"`
}

// frameworkScriptActions runs the post_composer hook and returns its validated
// plan. Values stay bindings until Go evaluates them, so a hook is not handed
// credentials and never has to escape anything itself.
func frameworkScriptActions(ctx PostComposerContext, plugin builtinFrameworkPlugin, database *DatabaseCredentials, root string) ([]frameworkActionManifest, error) {
	m := plugin.manifest
	module, err := plugin.scriptModule()
	if err != nil {
		return nil, err
	}
	bases := map[string]string{
		frameworkBaseAppRoot: root,
		frameworkBaseDocroot: frameworkDocrootPath(ctx, root, m.AppRoot.PublicDir),
	}
	owner := frameworkScriptOwner(m.ID)
	result, err := module.Call(frameworkHookPostComposer, nil, newFrameworkPostComposerContext(m.ID, ctx.Environment, database, bases))
	if err != nil {
		return nil, script.HookError(owner, m.Script.File, err)
	}
	actions, err := parseFrameworkPostComposerPlan(result)
	if err == nil {
		err = m.validateActions(actions, plugin.files)
	}
	if err != nil {
		return nil, fmt.Errorf("%s script %s %s: %w", owner, m.Script.File, frameworkHookPostComposer, err)
	}
	return actions, nil
}

// newFrameworkPostComposerContext builds the read-only ctx value passed to
// post_composer. It exposes where the database is but never credentials;
// scripts reference those through bindings such as {"from": "database.user"}.
func newFrameworkPostComposerContext(id string, environment config.Environment, database *DatabaseCredentials, bases map[string]string) starlark.Value {
	toolVersion := starlark.NewBuiltin("ctx.tool_version", func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var tool string
		if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &tool); err != nil {
			return nil, err
		}
		return starlark.String(environment.ToolVersion(tool)), nil
	})
	readText := starlark.NewBuiltin("ctx.read_text", func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		rawPath, base := "", frameworkBaseAppRoot
		if err := starlark.UnpackArgs(b.Name(), args, kwargs, "path", &rawPath, "base?", &base); err != nil {
			return nil, err
		}
		dir, ok := bases[base]
		if !ok {
			return nil, fmt.Errorf("%s: unsupported base %q", b.Name(), base)
		}
		return readFrameworkScriptFile(b.Name(), dir, rawPath)
	})

	fields := frameworkScriptFields(id, environment)
	fields["database"] = frameworkScriptDatabase(database)
	fields["tool_version"] = toolVersion
	fields["read_text"] = readText
	return script.NewStruct("ctx", fields)
}

// readFrameworkScriptFile reads a file for ctx.read_text, returning None when
// it or its base directory does not exist.
func readFrameworkScriptFile(name, dir, rawPath string) (starlark.Value, error) {
	if err := validateFrameworkManifestRelativePath(rawPath, "path", false); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return starlark.None, nil
		}
		return nil, script.Fail(fmt.Errorf("%s(%q): %w", name, rawPath, err))
	}
	defer root.Close()

	return script.ReadRootText(root, name, rawPath)
}

// frameworkScriptDatabase exposes the fields of database credentials that are
// not secrets, or None when the environment has no managed credentials.
func frameworkScriptDatabase(database *DatabaseCredentials) starlark.Value {
	if database == nil {
		return starlark.None
	}
	return script.NewStruct("database", starlark.StringDict{
		"engine":  starlark.String(database.Engine),
		"version": starlark.String(database.Version),
		"host":    starlark.String(database.Host),
		"port":    starlark.MakeInt(database.Port),
	})
}

// parseFrameworkPostComposerPlan decodes a hook result through its JSON form,
// which is valid YAML, so plans reuse the manifest decoders and their strict
// handling of unknown keys rather than a second parser for the same schema.
func parseFrameworkPostComposerPlan(result starlark.Value) ([]frameworkActionManifest, error) {
	if _, ok := result.(*starlark.Dict); !ok {
		return nil, fmt.Errorf("result must be a dict, got %s", result.Type())
	}
	encoded, err := script.EncodeJSON(result)
	if err != nil {
		return nil, err
	}
	var plan frameworkPostComposerPlan
	if err := yaml.UnmarshalWithOptions(encoded, &plan, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("parse result: %w", err)
	}
	return plan.Actions, nil
}

// frameworkScriptNginxConfig runs the nginx_config hook. A None result keeps
// the generic config.
func frameworkScriptNginxConfig(ctx NginxConfigContext, plugin builtinFrameworkPlugin) (NginxConfigResult, bool, error) {
	module, err := plugin.scriptModule()
	if err != nil {
		return NginxConfigResult{}, false, err
	}
	owner := frameworkScriptOwner(plugin.id)
	file := plugin.manifest.Script.File
	result, err := module.Call(frameworkHookNginxConfig, nil, newFrameworkNginxContext(plugin.id, ctx))
	if err != nil {
		return NginxConfigResult{}, false, script.HookError(owner, file, err)
	}
	if result == starlark.None {
		return NginxConfigResult{}, false, nil
	}
	text, ok := starlark.AsString(result)
	if !ok {
		return NginxConfigResult{}, false, fmt.Errorf("%s script %s %s: result must be a string or None, got %s", owner, file, frameworkHookNginxConfig, result.Type())
	}
	if strings.TrimSpace(text) == "" {
		return NginxConfigResult{}, false, fmt.Errorf("%s script %s %s: result cannot be empty; return None to keep the generic config", owner, file, frameworkHookNginxConfig)
	}
	return NginxConfigResult{Config: []byte(text)}, true, nil
}

// newFrameworkNginxContext builds the read-only ctx value passed to nginx_config.
func newFrameworkNginxContext(id string, ctx NginxConfigContext) starlark.Value {
	var tls starlark.Value = starlark.None
	if ctx.TLSEnabled {
		tls = script.NewStruct("tls", starlark.StringDict{
			"certificate":     starlark.String(ctx.TLSCertificatePath),
			"certificate_key": starlark.String(ctx.TLSCertificateKeyPath),
		})
	}

	fields := frameworkScriptFields(id, ctx.Environment)
	fields["host"] = starlark.String(ctx.Host)
	fields["port"] = starlark.MakeInt(ctx.Port)
	fields["docroot"] = starlark.String(ctx.Docroot)
	fields["index_names"] = starlark.String(ctx.IndexNames)
	fields["try_files_fallback"] = starlark.String(ctx.TryFilesFallback)
	fields["fastcgi_index"] = starlark.String(ctx.FastCGIIndex)
	fields["backend_address"] = starlark.String(ctx.BackendAddress)
	fields["tls"] = tls
	fields["default_config"] = starlark.String(ctx.DefaultConfig)
	return script.NewStruct("ctx", fields)
}
