// Package script provides the sandboxed Starlark runtime used by Polka plugin
// scripts. It knows nothing about Polka plugins: callers supply domain modules
// through Options.Predeclared and decide which global functions are hooks.
//
// Scripts run with a step budget, cannot load other files, and have no access
// to the filesystem, environment, or processes unless a predeclared module
// grants it. Loaded modules are frozen, so one Module may be called from many
// goroutines at once.
package script

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// DefaultMaxSteps bounds the Starlark computation steps of one module load or
// one hook call when Options.MaxSteps is zero.
const DefaultMaxSteps uint64 = 5_000_000

// Options customizes how a script module is loaded and called.
type Options struct {
	// Predeclared adds domain modules or values visible to the script. Entries
	// are applied after the built-in modules and may replace them.
	Predeclared starlark.StringDict
	// MaxSteps overrides DefaultMaxSteps when non-zero.
	MaxSteps uint64
	// Print receives print() output. Nil discards it.
	Print func(msg string)
}

// Module is a loaded, frozen Starlark script.
type Module struct {
	path    string
	globals starlark.StringDict
	opts    Options
}

// FailError is a deliberate, user-facing script failure raised by fail() or by
// a host builtin that wraps its error with Fail. Callers report it verbatim,
// without a Starlark backtrace, unlike other evaluation errors that indicate a
// bug in the script.
type FailError struct {
	Msg string
	Err error
}

// Error returns the failure message.
func (e *FailError) Error() string { return e.Msg }

// Unwrap returns the host error wrapped by Fail, if any.
func (e *FailError) Unwrap() error { return e.Err }

// Fail wraps a host error so it is reported to users verbatim, like fail().
func Fail(err error) error {
	return &FailError{Msg: err.Error(), Err: err}
}

// Describe formats err for plugin authors, including the Starlark backtrace
// when err came from script evaluation.
func Describe(err error) string {
	var evalErr *starlark.EvalError
	if errors.As(err, &evalErr) {
		return evalErr.Backtrace()
	}

	return err.Error()
}

// Load reads, compiles, and executes the script at path within fsys, then
// freezes its globals. The path uses fs.FS slash-separated syntax.
func Load(fsys fs.FS, path string, opts Options) (*Module, error) {
	if fsys == nil {
		return nil, fmt.Errorf("load script %s: no filesystem", path)
	}
	if !fs.ValidPath(path) {
		return nil, fmt.Errorf("load script %s: invalid path", path)
	}
	source, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, fmt.Errorf("load script %s: %w", path, err)
	}

	predeclared := predeclaredValues(opts)
	// The zero FileOptions keep the conservative dialect: no while loops, no
	// recursion, no top-level control flow, and no global reassignment.
	_, program, err := starlark.SourceProgramOptions(&syntax.FileOptions{}, path, source, predeclared.Has)
	if err != nil {
		return nil, fmt.Errorf("compile script %s: %w", path, err)
	}

	module := &Module{path: path, opts: opts}
	globals, err := program.Init(module.newThread(path, nil), predeclared)
	if err != nil {
		return nil, fmt.Errorf("execute script %s: %s", path, Describe(err))
	}
	globals.Freeze()
	module.globals = globals

	return module, nil
}

// Path returns the script path the module was loaded from.
func (m *Module) Path() string {
	return m.path
}

// Names returns the module's global names in sorted order.
func (m *Module) Names() []string {
	names := make([]string, 0, len(m.globals))
	for name := range m.globals {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

// Global returns the frozen global value bound to name, or nil.
func (m *Module) Global(name string) starlark.Value {
	return m.globals[name]
}

// Call invokes the global function name with positional args on a fresh
// thread. locals become thread-local values readable by host builtins through
// starlark.Thread.Local. The returned error may wrap a *FailError.
func (m *Module) Call(name string, locals map[string]any, args ...starlark.Value) (starlark.Value, error) {
	fn, ok := m.globals[name]
	if !ok {
		return nil, fmt.Errorf("script %s does not define %s", m.path, name)
	}
	if _, ok := fn.(starlark.Callable); !ok {
		return nil, fmt.Errorf("script %s global %s is not callable", m.path, name)
	}

	return starlark.Call(m.newThread(m.path+":"+name, locals), fn, starlark.Tuple(args), nil)
}

// newThread creates a sandboxed thread with the module's step budget, print
// handler, and the supplied thread-local values.
func (m *Module) newThread(name string, locals map[string]any) *starlark.Thread {
	thread := &starlark.Thread{
		Name: name,
		Print: func(_ *starlark.Thread, msg string) {
			if m.opts.Print != nil {
				m.opts.Print(msg)
			}
		},
		Load: func(_ *starlark.Thread, module string) (starlark.StringDict, error) {
			return nil, fmt.Errorf("load(%q) is not supported in plugin scripts", module)
		},
	}
	maxSteps := m.opts.MaxSteps
	if maxSteps == 0 {
		maxSteps = DefaultMaxSteps
	}
	thread.SetMaxExecutionSteps(maxSteps)
	for key, value := range locals {
		thread.SetLocal(key, value)
	}

	return thread
}

// predeclaredValues merges the built-in modules with caller-supplied values
// and freezes the result so it can be shared across threads.
func predeclaredValues(opts Options) starlark.StringDict {
	predeclared := starlark.StringDict{
		"fail": starlark.NewBuiltin("fail", failBuiltin),
		"html": htmlModule,
		"json": jsonModule,
		"re":   reModule,
		"url":  urlModule,
	}
	for name, value := range opts.Predeclared {
		predeclared[name] = value
	}
	predeclared.Freeze()

	return predeclared
}

// failBuiltin replaces Starlark's universal fail so deliberate failures carry
// a *FailError without the "fail: " prefix.
func failBuiltin(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	sep := " "
	if err := starlark.UnpackArgs(b.Name(), nil, kwargs, "sep?", &sep); err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		if text, ok := starlark.AsString(arg); ok {
			parts = append(parts, text)
		} else {
			parts = append(parts, arg.String())
		}
	}

	return nil, &FailError{Msg: strings.Join(parts, sep)}
}
