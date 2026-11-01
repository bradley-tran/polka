package script

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"

	starlarkjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// jsonModule exposes json.encode, json.decode, and json.indent.
var jsonModule = starlarkjson.Module

// htmlModule exposes HTML text helpers.
var htmlModule = &starlarkstruct.Module{
	Name: "html",
	Members: starlark.StringDict{
		"unescape": starlark.NewBuiltin("html.unescape", htmlUnescape),
	},
}

// reModule exposes RE2 regular expressions, which match in linear time.
var reModule = &starlarkstruct.Module{
	Name: "re",
	Members: starlark.StringDict{
		"find_all": starlark.NewBuiltin("re.find_all", reFindAll),
		"search":   starlark.NewBuiltin("re.search", reSearch),
	},
}

// urlModule exposes URL helpers.
var urlModule = &starlarkstruct.Module{
	Name: "url",
	Members: starlark.StringDict{
		"resolve": starlark.NewBuiltin("url.resolve", urlResolve),
	},
}

// htmlUnescape implements html.unescape(s).
func htmlUnescape(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var text string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &text); err != nil {
		return nil, err
	}

	return starlark.String(html.UnescapeString(text)), nil
}

// reFindAll implements re.find_all(pattern, s), returning one list per match
// holding the whole match followed by its capture groups.
func reFindAll(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	matcher, text, err := unpackRegexpArgs(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	matches := matcher.FindAllStringSubmatch(text, -1)
	values := make([]starlark.Value, 0, len(matches))
	for _, match := range matches {
		values = append(values, stringList(match))
	}

	return starlark.NewList(values), nil
}

// reSearch implements re.search(pattern, s), returning the first match and its
// capture groups as a list, or None when the pattern does not match.
func reSearch(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	matcher, text, err := unpackRegexpArgs(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	match := matcher.FindStringSubmatch(text)
	if match == nil {
		return starlark.None, nil
	}

	return stringList(match), nil
}

// unpackRegexpArgs unpacks (pattern, s) and compiles pattern.
func unpackRegexpArgs(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (*regexp.Regexp, string, error) {
	var pattern, text string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 2, &pattern, &text); err != nil {
		return nil, "", err
	}
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", b.Name(), err)
	}

	return matcher, text, nil
}

// urlResolve implements url.resolve(base, ref) per RFC 3986.
func urlResolve(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var base, ref string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 2, &base, &ref); err != nil {
		return nil, err
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("%s: parse base: %w", b.Name(), err)
	}
	refURL, err := url.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("%s: parse reference: %w", b.Name(), err)
	}

	return starlark.String(baseURL.ResolveReference(refURL).String()), nil
}

// stringList converts Go strings to a Starlark list.
func stringList(values []string) *starlark.List {
	items := make([]starlark.Value, 0, len(values))
	for _, value := range values {
		items = append(items, starlark.String(value))
	}

	return starlark.NewList(items)
}

// StringMap converts a Go string map to a Starlark dict with sorted keys so
// iteration order is deterministic.
func StringMap(values map[string]string) *starlark.Dict {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	dict := starlark.NewDict(len(keys))
	for _, key := range keys {
		// SetKey only fails for unhashable keys or frozen dicts; neither
		// applies to a fresh dict with string keys.
		if err := dict.SetKey(starlark.String(key), starlark.String(values[key])); err != nil {
			panic(err)
		}
	}

	return dict
}

// ToStringMap converts a Starlark dict with string keys and values to a Go
// map. None converts to nil. what names the value in error messages.
func ToStringMap(value starlark.Value, what string) (map[string]string, error) {
	if value == starlark.None {
		return nil, nil
	}
	dict, ok := value.(*starlark.Dict)
	if !ok {
		return nil, fmt.Errorf("%s must be a dict, got %s", what, value.Type())
	}

	values := make(map[string]string, dict.Len())
	for _, item := range dict.Items() {
		key, ok := starlark.AsString(item[0])
		if !ok {
			return nil, fmt.Errorf("%s keys must be strings, got %s", what, item[0].Type())
		}
		text, ok := starlark.AsString(item[1])
		if !ok {
			return nil, fmt.Errorf("%s[%q] must be a string, got %s", what, key, item[1].Type())
		}
		values[key] = text
	}

	return values, nil
}

// NewStruct builds a frozen struct value whose type name is name, suitable
// for passing read-only context objects to hooks.
func NewStruct(name string, fields starlark.StringDict) *starlarkstruct.Struct {
	value := starlarkstruct.FromStringDict(starlark.String(name), fields)
	value.Freeze()

	return value
}
