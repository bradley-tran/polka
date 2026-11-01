package script

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"go.starlark.net/starlark"
)

// loadTestModule loads source as main.star from an in-memory filesystem.
func loadTestModule(t *testing.T, source string, opts Options) *Module {
	t.Helper()
	module, err := Load(fstest.MapFS{"main.star": {Data: []byte(source)}}, "main.star", opts)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	return module
}

// TestLoadAndCallReturnsValue verifies the basic load-then-call round trip.
func TestLoadAndCallReturnsValue(t *testing.T) {
	module := loadTestModule(t, "def double(x):\n    return x * 2\n", Options{})

	result, err := module.Call("double", nil, starlark.MakeInt(21))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if result.String() != "42" {
		t.Fatalf("Call() = %s, want 42", result)
	}
	if got := module.Names(); len(got) != 1 || got[0] != "double" {
		t.Fatalf("Names() = %#v, want [double]", got)
	}
}

// TestLoadRejectsInvalidInputs covers missing files, invalid paths, syntax
// errors, and failing top-level code.
func TestLoadRejectsInvalidInputs(t *testing.T) {
	fsys := fstest.MapFS{
		"syntax.star":  {Data: []byte("def broken(:\n")},
		"runtime.star": {Data: []byte("value = 1 // 0\n")},
		"while.star":   {Data: []byte("def spin():\n    while True:\n        pass\n")},
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "missing", path: "missing.star", want: "load script missing.star"},
		{name: "invalid path", path: "../escape.star", want: "invalid path"},
		{name: "syntax", path: "syntax.star", want: "compile script syntax.star"},
		{name: "runtime", path: "runtime.star", want: "division by zero"},
		{name: "while disabled", path: "while.star", want: "while"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(fsys, test.path, Options{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load(%s) error = %v, want containing %q", test.path, err, test.want)
			}
		})
	}
}

// TestLoadStatementIsRejected verifies scripts cannot load other files.
func TestLoadStatementIsRejected(t *testing.T) {
	fsys := fstest.MapFS{
		"main.star":   {Data: []byte("load(\"helper.star\", \"x\")\n")},
		"helper.star": {Data: []byte("x = 1\n")},
	}

	_, err := Load(fsys, "main.star", Options{})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("Load() error = %v, want load() rejection", err)
	}
}

// TestCallEnforcesStepBudget verifies runaway hooks are cancelled.
func TestCallEnforcesStepBudget(t *testing.T) {
	module := loadTestModule(t, "def spin():\n    total = 0\n    for i in range(1000000):\n        total += i\n    return total\n", Options{MaxSteps: 1000})

	_, err := module.Call("spin", nil)
	if err == nil || !strings.Contains(err.Error(), "too many steps") {
		t.Fatalf("Call() error = %v, want step budget error", err)
	}
}

// TestFailProducesFailError verifies fail() is distinguishable from script
// bugs and carries no "fail: " prefix.
func TestFailProducesFailError(t *testing.T) {
	module := loadTestModule(t, "def check(x):\n    if x:\n        fail(\"bad\", \"value\")\n    return [][1]\n", Options{})

	_, err := module.Call("check", nil, starlark.True)
	var failErr *FailError
	if !errors.As(err, &failErr) {
		t.Fatalf("Call(True) error = %v, want *FailError", err)
	}
	if failErr.Msg != "bad value" {
		t.Fatalf("FailError.Msg = %q, want %q", failErr.Msg, "bad value")
	}

	_, err = module.Call("check", nil, starlark.False)
	if err == nil || errors.As(err, &failErr) {
		t.Fatalf("Call(False) error = %v, want non-fail evaluation error", err)
	}
	if detail := Describe(err); !strings.Contains(detail, "Traceback") || !strings.Contains(detail, "main.star") {
		t.Fatalf("Describe() = %q, want backtrace naming main.star", detail)
	}
}

// TestFailWrapsHostErrors verifies Fail keeps the host error unwrappable.
func TestFailWrapsHostErrors(t *testing.T) {
	cause := errors.New("network down")
	err := Fail(cause)
	if !errors.Is(err, cause) || err.Error() != "network down" {
		t.Fatalf("Fail() = %v, want verbatim wrapped cause", err)
	}
}

// TestGlobalsAreFrozen verifies hooks cannot mutate shared module state.
func TestGlobalsAreFrozen(t *testing.T) {
	module := loadTestModule(t, "items = []\ndef add():\n    items.append(1)\n", Options{})

	_, err := module.Call("add", nil)
	if err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("Call() error = %v, want frozen mutation error", err)
	}
}

// TestThreadLocalsReachHostBuiltins verifies Call passes locals to builtins.
func TestThreadLocalsReachHostBuiltins(t *testing.T) {
	probe := starlark.NewBuiltin("probe", func(thread *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		value, _ := thread.Local("key").(string)
		return starlark.String(value), nil
	})
	module := loadTestModule(t, "def read():\n    return probe()\n", Options{Predeclared: starlark.StringDict{"probe": probe}})

	result, err := module.Call("read", map[string]any{"key": "secret"})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got, _ := starlark.AsString(result); got != "secret" {
		t.Fatalf("Call() = %s, want secret", result)
	}
}

// TestPrintIsCapturedOrDiscarded verifies print() never reaches stdout.
func TestPrintIsCapturedOrDiscarded(t *testing.T) {
	var printed []string
	module := loadTestModule(t, "def say():\n    print(\"hello\")\n", Options{Print: func(msg string) { printed = append(printed, msg) }})
	if _, err := module.Call("say", nil); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(printed) != 1 || printed[0] != "hello" {
		t.Fatalf("printed = %#v, want [hello]", printed)
	}

	silent := loadTestModule(t, "def say():\n    print(\"hello\")\n", Options{})
	if _, err := silent.Call("say", nil); err != nil {
		t.Fatalf("Call() without Print error = %v", err)
	}
}

// TestBuiltinModules covers the re, url, html, and json helpers.
func TestBuiltinModules(t *testing.T) {
	module := loadTestModule(t, `
def run():
    return {
        "find_all": re.find_all(r"v(\d+)\.(\d+)", "v1.2 v3.4"),
        "search": re.search(r"(\d+)", "abc 42"),
        "miss": re.search(r"\d", "abc"),
        "url": url.resolve("https://example.com/download/", "/files/a.zip"),
        "html": html.unescape("a&amp;b"),
        "json": json.decode('{"k": [1, 2]}')["k"][1],
    }
`, Options{})

	result, err := module.Call("run", nil)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	dict := result.(*starlark.Dict)
	want := map[string]string{
		"find_all": `[["v1.2", "1", "2"], ["v3.4", "3", "4"]]`,
		"search":   `["42", "42"]`,
		"miss":     "None",
		"url":      `"https://example.com/files/a.zip"`,
		"html":     `"a&b"`,
		"json":     "2",
	}
	for key, expected := range want {
		value, found, err := dict.Get(starlark.String(key))
		if err != nil || !found {
			t.Fatalf("result[%s] missing: %v", key, err)
		}
		if value.String() != expected {
			t.Fatalf("result[%s] = %s, want %s", key, value, expected)
		}
	}
}

// TestBuiltinModuleErrors verifies invalid regexps and URLs surface errors.
func TestBuiltinModuleErrors(t *testing.T) {
	module := loadTestModule(t, "def bad_re():\n    return re.search(\"(\", \"x\")\ndef bad_url():\n    return url.resolve(\"https://example.com\", \"%zz\")\n", Options{})

	if _, err := module.Call("bad_re", nil); err == nil || !strings.Contains(err.Error(), "re.search") {
		t.Fatalf("bad_re error = %v, want re.search error", err)
	}
	if _, err := module.Call("bad_url", nil); err == nil || !strings.Contains(err.Error(), "url.resolve") {
		t.Fatalf("bad_url error = %v, want url.resolve error", err)
	}
}

// TestStringMapRoundTrip verifies Go/Starlark string map conversion.
func TestStringMapRoundTrip(t *testing.T) {
	values, err := ToStringMap(StringMap(map[string]string{"b": "2", "a": "1"}), "vars")
	if err != nil {
		t.Fatalf("ToStringMap() error = %v", err)
	}
	if len(values) != 2 || values["a"] != "1" || values["b"] != "2" {
		t.Fatalf("ToStringMap() = %#v", values)
	}
	if values, err := ToStringMap(starlark.None, "vars"); err != nil || values != nil {
		t.Fatalf("ToStringMap(None) = %#v, %v; want nil, nil", values, err)
	}

	bad := starlark.NewDict(1)
	if err := bad.SetKey(starlark.String("k"), starlark.MakeInt(1)); err != nil {
		t.Fatalf("SetKey() error = %v", err)
	}
	if _, err := ToStringMap(bad, "vars"); err == nil || !strings.Contains(err.Error(), `vars["k"] must be a string`) {
		t.Fatalf("ToStringMap(non-string) error = %v", err)
	}
	if _, err := ToStringMap(starlark.MakeInt(1), "vars"); err == nil {
		t.Fatal("ToStringMap(int) unexpectedly succeeded")
	}
}

// TestConcurrentCalls verifies one module can serve parallel hook calls.
func TestConcurrentCalls(t *testing.T) {
	module := loadTestModule(t, "table = {\"a\": 1}\ndef lookup(key):\n    return table.get(key, 0) + len([x for x in range(100)])\n", Options{})

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := module.Call("lookup", nil, starlark.String("a"))
			if err == nil && result.String() != "101" {
				err = errors.New("unexpected result " + result.String())
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Call() error = %v", err)
		}
	}
}
