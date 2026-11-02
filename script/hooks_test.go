package script

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"go.starlark.net/starlark"
)

// TestLoadHooksChecksPublicFunctions verifies a script's public functions must
// match the known and declared hooks.
func TestLoadHooksChecksPublicFunctions(t *testing.T) {
	known := []string{"first", "second"}
	for _, test := range []struct {
		name, source string
		declared     []string
		want         string
	}{
		{name: "declared hook", source: "def first(ctx):\n    pass\n", declared: []string{"first"}},
		{name: "helpers and constants", source: "LIMIT = 1\ndef _helper():\n    pass\ndef first(ctx):\n    pass\n", declared: []string{"first"}},
		{name: "undeclared hook", source: "def first(ctx):\n    pass\ndef second(ctx):\n    pass\n", declared: []string{"first"}, want: `demo script hook.star defines hook "second" that is not declared in script.hooks`},
		{name: "public helper", source: "def first(ctx):\n    pass\ndef helper():\n    pass\n", declared: []string{"first"}, want: `demo script hook.star defines unsupported public function "helper"; prefix helpers with _`},
		{name: "missing hook", source: "LIMIT = 1\n", declared: []string{"first"}, want: `demo script hook.star declares hook "first" but does not define it as a function`},
		{name: "not a function", source: "first = 1\n", declared: []string{"first"}, want: `declares hook "first" but does not define it as a function`},
		{name: "no parameter", source: "def first():\n    pass\n", declared: []string{"first"}, want: `demo script hook.star hook "first" must take exactly one parameter`},
		{name: "two parameters", source: "def first(a, b):\n    pass\n", declared: []string{"first"}, want: "exactly one parameter"},
		{name: "varargs", source: "def first(*args):\n    pass\n", declared: []string{"first"}, want: "exactly one parameter"},
		{name: "kwargs", source: "def first(ctx, **kwargs):\n    pass\n", declared: []string{"first"}, want: "exactly one parameter"},
		{name: "compile error", source: "def first(ctx:\n", declared: []string{"first"}, want: "demo script: compile script hook.star"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fsys := fstest.MapFS{"hook.star": {Data: []byte(test.source)}}
			module, err := LoadHooks(fsys, "hook.star", "demo", known, test.declared, Options{})
			if test.want == "" {
				if err != nil || module == nil {
					t.Fatalf("LoadHooks() = %v, %v", module, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadHooks() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestHookErrorSeparatesFailuresFromBugs verifies fail() stays verbatim while a
// script bug names its owner and carries a backtrace.
func TestHookErrorSeparatesFailuresFromBugs(t *testing.T) {
	source := "def deliberate(ctx):\n    fail(\"expected failure\")\ndef bug(ctx):\n    return ctx.missing\n"
	fsys := fstest.MapFS{"hook.star": {Data: []byte(source)}}
	module, err := LoadHooks(fsys, "hook.star", "demo", []string{"deliberate", "bug"}, []string{"deliberate", "bug"}, Options{})
	if err != nil {
		t.Fatalf("LoadHooks() error = %v", err)
	}

	_, callErr := module.Call("deliberate", nil, starlark.None)
	got := HookError("demo", "hook.star", callErr)
	var failErr *FailError
	if !errors.As(got, &failErr) || got.Error() != "expected failure" {
		t.Fatalf("HookError(fail) = %#v, want the message verbatim", got)
	}

	_, callErr = module.Call("bug", nil, starlark.None)
	got = HookError("demo", "hook.star", callErr)
	if errors.As(got, &failErr) {
		t.Fatalf("HookError(bug) = %#v, want a plain error", got)
	}
	for _, want := range []string{"demo script hook.star: ", "Traceback"} {
		if !strings.Contains(got.Error(), want) {
			t.Fatalf("HookError(bug) = %q, want %q", got, want)
		}
	}
}

// TestReadRootText covers contents, absence, the size limit, and read failures.
func TestReadRootText(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "limit.txt"), make([]byte, MaxReadTextBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "huge.txt"), make([]byte, MaxReadTextBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	value, err := ReadRootText(root, "read", "file.txt")
	if err != nil || value != starlark.String("contents") {
		t.Fatalf("ReadRootText(file) = %v, %v", value, err)
	}
	value, err = ReadRootText(root, "read", "missing/file.txt")
	if err != nil || value != starlark.None {
		t.Fatalf("ReadRootText(missing) = %v, %v, want None", value, err)
	}
	value, err = ReadRootText(root, "read", "limit.txt")
	if text, ok := starlark.AsString(value); err != nil || !ok || len(text) != MaxReadTextBytes {
		t.Fatalf("ReadRootText(limit) length = %d, %v", len(text), err)
	}

	// Read failures are user-facing rather than script bugs.
	var failErr *FailError
	if _, err := ReadRootText(root, "read", "huge.txt"); !errors.As(err, &failErr) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("ReadRootText(huge) error = %v, want a size failure", err)
	}
	if _, err := ReadRootText(root, "read", "directory"); !errors.As(err, &failErr) {
		t.Fatalf("ReadRootText(directory) error = %v, want a failure", err)
	}
	if _, err := ReadRootText(root, "read", "../outside.txt"); !errors.As(err, &failErr) {
		t.Fatalf("ReadRootText(escape) error = %v, want a failure", err)
	}
}

// TestEncodeJSON verifies hook results encode as JSON and that values JSON
// cannot represent are rejected.
func TestEncodeJSON(t *testing.T) {
	dict := starlark.NewDict(1)
	if err := dict.SetKey(starlark.String("key"), starlark.NewList([]starlark.Value{starlark.None, starlark.True, starlark.MakeInt(1)})); err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeJSON(dict)
	if err != nil || string(encoded) != `{"key":[null,true,1]}` {
		t.Fatalf("EncodeJSON() = %s, %v", encoded, err)
	}

	if _, err := EncodeJSON(starlark.NewBuiltin("fn", nil)); err == nil {
		t.Fatal("EncodeJSON(builtin) error = nil, want an error")
	}
}
