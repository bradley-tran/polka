package tools

import (
	"fmt"
	"net/http"

	starlarkjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"polka/script"
)

const (
	// scriptHTTPLocal is the thread-local key granting a hook call network
	// access. Only the resolve and build_sources hooks set it.
	scriptHTTPLocal = "polka.http"
	// scriptHTTPMaxBytes caps one script HTTP response body.
	scriptHTTPMaxBytes = 8 << 20
)

// scriptHTTPAccess is the thread-local value that enables the http module.
type scriptHTTPAccess struct {
	client *http.Client
	tool   string
}

// toolScriptModules returns the Polka-specific modules predeclared for tool
// scripts in addition to the generic script modules.
func toolScriptModules() starlark.StringDict {
	return starlark.StringDict{
		"crypto":  scriptCryptoModule,
		"http":    scriptHTTPModule,
		"path":    scriptPathModule,
		"version": scriptVersionModule,
	}
}

// scriptVersionModule exposes Polka's version label helpers.
var scriptVersionModule = &starlarkstruct.Module{
	Name: "version",
	Members: starlark.StringDict{
		"compare": starlark.NewBuiltin("version.compare", scriptVersionCompare),
		"matches": starlark.NewBuiltin("version.matches", scriptVersionMatches),
		"best":    starlark.NewBuiltin("version.best", scriptVersionBest),
	},
}

// scriptHTTPModule exposes read-only GET requests to the resolve and
// build_sources hooks.
var scriptHTTPModule = &starlarkstruct.Module{
	Name: "http",
	Members: starlark.StringDict{
		"get":      starlark.NewBuiltin("http.get", scriptHTTPGet),
		"get_json": starlark.NewBuiltin("http.get_json", scriptHTTPGetJSON),
	},
}

// scriptVersionCompare implements version.compare(a, b) -> -1, 0, or 1.
func scriptVersionCompare(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var left, right string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 2, &left, &right); err != nil {
		return nil, err
	}

	return starlark.MakeInt(compareVersions(left, right)), nil
}

// scriptVersionMatches implements version.matches(version, requested), true
// when version equals requested or extends it with more dot segments.
func scriptVersionMatches(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var version, requested string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 2, &version, &requested); err != nil {
		return nil, err
	}

	return starlark.Bool(versionMatchesRequest(version, requested)), nil
}

// scriptVersionBest implements version.best(versions, requested), returning
// the newest version matching requested or None when nothing matches.
func scriptVersionBest(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var iterable starlark.Iterable
	var requested string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 2, &iterable, &requested); err != nil {
		return nil, err
	}

	versions := []string{}
	iterator := iterable.Iterate()
	defer iterator.Done()
	var item starlark.Value
	for iterator.Next(&item) {
		text, ok := starlark.AsString(item)
		if !ok {
			return nil, fmt.Errorf("%s: versions must be strings, got %s", b.Name(), item.Type())
		}
		versions = append(versions, text)
	}
	best, err := resolveMatchingVersion(versions, requested)
	if err != nil {
		// No match (or an empty request) is an expected outcome the script
		// decides how to report.
		return starlark.None, nil
	}

	return starlark.String(best), nil
}

// scriptHTTPGet implements http.get(url) -> response body text.
func scriptHTTPGet(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	text, err := scriptHTTPText(thread, b, args, kwargs)
	if err != nil {
		return nil, err
	}

	return starlark.String(text), nil
}

// scriptHTTPGetJSON implements http.get_json(url) -> decoded JSON value.
func scriptHTTPGetJSON(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	text, err := scriptHTTPText(thread, b, args, kwargs)
	if err != nil {
		return nil, err
	}
	decoded, err := starlark.Call(thread, starlarkjson.Module.Members["decode"], starlark.Tuple{starlark.String(text)}, nil)
	if err != nil {
		// Malformed upstream metadata is an expected failure, not a script bug.
		return nil, script.Fail(fmt.Errorf("%s: %w", b.Name(), err))
	}

	return decoded, nil
}

// scriptHTTPText fetches url for an http builtin. Network failures are
// user-facing, so they are wrapped with script.Fail rather than reported as
// script bugs.
func scriptHTTPText(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (string, error) {
	var rawURL string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &rawURL); err != nil {
		return "", err
	}
	access, ok := thread.Local(scriptHTTPLocal).(scriptHTTPAccess)
	if !ok {
		return "", fmt.Errorf("%s: http is only available in the resolve and build_sources hooks", b.Name())
	}
	if err := validateScriptURL(rawURL); err != nil {
		return "", fmt.Errorf("%s: %w", b.Name(), err)
	}

	text, err := downloadTextLimit(access.client, rawURL, access.tool+" release metadata", scriptHTTPMaxBytes)
	if err != nil {
		return "", script.Fail(err)
	}

	return text, nil
}
