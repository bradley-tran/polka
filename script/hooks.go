package script

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.starlark.net/starlark"
)

// MaxReadTextBytes caps one file read through ReadRootText.
const MaxReadTextBytes = 8 << 20

// LoadHooks loads the script at file and checks its public functions against
// the hooks a manifest declares. known lists every hook the plugin kind
// supports, and declared lists those this manifest implements. Other public
// functions are rejected so a misspelled hook fails instead of being silently
// ignored. owner names the plugin in error messages, such as "sqlite".
func LoadHooks(fsys fs.FS, file, owner string, known, declared []string, opts Options) (*Module, error) {
	module, err := Load(fsys, file, opts)
	if err != nil {
		return nil, fmt.Errorf("%s script: %w", owner, err)
	}

	for _, name := range module.Names() {
		if strings.HasPrefix(name, "_") {
			continue
		}
		if _, callable := module.Global(name).(starlark.Callable); !callable {
			continue
		}
		if !slices.Contains(known, name) {
			return nil, fmt.Errorf("%s script %s defines unsupported public function %q; prefix helpers with _", owner, file, name)
		}
		if !slices.Contains(declared, name) {
			return nil, fmt.Errorf("%s script %s defines hook %q that is not declared in script.hooks", owner, file, name)
		}
	}
	for _, hook := range declared {
		fn, ok := module.Global(hook).(*starlark.Function)
		if !ok {
			return nil, fmt.Errorf("%s script %s declares hook %q but does not define it as a function", owner, file, hook)
		}
		if fn.NumParams() != 1 || fn.HasVarargs() || fn.HasKwargs() || fn.NumKwonlyParams() != 0 {
			return nil, fmt.Errorf("%s script %s hook %q must take exactly one parameter", owner, file, hook)
		}
	}

	return module, nil
}

// HookError converts a hook failure into a Polka error. fail() messages are
// user-facing and returned verbatim; anything else is a bug in the script and
// is reported with its Starlark backtrace.
func HookError(owner, file string, err error) error {
	var failErr *FailError
	if errors.As(err, &failErr) {
		return failErr
	}

	return fmt.Errorf("%s script %s: %s", owner, file, Describe(err))
}

// ReadRootText reads the slash-separated path inside root for a read_text
// builtin called name, returning None when the file does not exist. os.Root
// refuses anything that resolves outside root, including through symlinks.
// Failures reading the file are not script bugs, so they are reported like
// fail().
func ReadRootText(root *os.Root, name, rawPath string) (starlark.Value, error) {
	file, err := root.Open(filepath.FromSlash(rawPath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return starlark.None, nil
		}
		return nil, Fail(fmt.Errorf("%s(%q): %w", name, rawPath, err))
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, MaxReadTextBytes+1))
	if err != nil {
		return nil, Fail(fmt.Errorf("%s(%q): %w", name, rawPath, err))
	}
	if len(data) > MaxReadTextBytes {
		return nil, Fail(fmt.Errorf("%s(%q): file exceeds %d bytes", name, rawPath, MaxReadTextBytes))
	}

	return starlark.String(data), nil
}
