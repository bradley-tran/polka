# Tool scripts

A tool manifest (`tools/manifests/<id>.yaml`) can delegate logic the YAML schema cannot express to a sandboxed [Starlark](https://github.com/google/starlark-go) script. Scripts are embedded with the manifests under `tools/manifests/scripts/*.star` and replace per-tool Go hooks for version discovery and cross-field validation. The loader reads manifests and scripts through an `fs.FS`, but Polka still does not load plugins from disk.

Scripts currently implement two hooks:

| Hook | Replaces | Runs |
| --- | --- | --- |
| `resolve(ctx)` | a Go `download` hook that scrapes a release listing | when a tool version is downloaded into the cache |
| `validate(env)` | a Go `validate` hook | whenever an environment is validated, only if the tool has a version or settings |

Dependencies, post-install work, and framework plugins remain declarative YAML or Go.

## Manifest schema

```yaml
id: sqlite
download:
  assets:
    windows-amd64:
      filename: sqlite-tools-win-x64-{sqlite_numeric}.zip
      url: https://www.sqlite.org/{sqlite_relative_url}
      checksum-algorithm: sha3-256
      archive-format: zip
script:
  file: scripts/sqlite.star
  hooks: [resolve]
  vars:
    download-page: https://www.sqlite.org/download.html
```

- `file` is a `.star` path relative to the manifest directory. Absolute paths and paths escaping that directory are rejected, and the file must exist.
- `hooks` lists the hooks the script implements: `resolve`, `validate`, or both, without duplicates. Declaring hooks keeps manifest validation static, because scripts are only compiled when a hook first runs.
- `vars` is an optional string map handed to hooks as `ctx.vars`. Keys are lowercased. Put upstream URLs here so the hosts a script contacts stay visible in YAML and tests can point them at local servers.
- A `resolve` hook requires `download.assets` and cannot be combined with `download.github` or `download.release-index`.
- A tool cannot implement the same hook in both Go and Starlark.

## Script structure

```python
_PREFIXES = {"windows-amd64": "sqlite-tools-win-x64-"}

def _helper(text):
    return text.strip()

def resolve(ctx):
    ...
```

Each declared hook must be a top-level function taking exactly one parameter. Other top-level functions must start with `_`, so a misspelled hook name fails instead of being silently ignored. Constants may be public.

Polka uses the conservative Starlark dialect: no `while`, no recursion, no top-level `if`/`for`, and no global reassignment. Globals are frozen after the script loads, so hooks cannot keep state between calls.

## `resolve(ctx)`

`ctx` is read-only:

| Field | Value |
| --- | --- |
| `ctx.tool` | tool ID |
| `ctx.requested` | requested version label, such as `3.53` |
| `ctx.os`, `ctx.arch` | target platform, such as `windows` and `amd64` |
| `ctx.platform` | `<os>-<arch>`, matching `download.assets` keys |
| `ctx.vars` | the manifest's `script.vars` |

The hook returns a dict. Unknown keys are rejected, and `None` values count as unset:

| Key | Meaning |
| --- | --- |
| `version` | required resolved version; names the cache entry, so it must be a valid version label and satisfy `version-constraint` |
| `tag` | value for the `{tag}` placeholder; defaults to `version` |
| `values` | extra `{placeholder}` values for the asset templates; names use letters, numbers, and underscores and cannot override `version`, `requested`, `tag`, `major`, `minor`, or `major_minor` |
| `url` | absolute http(s) URL replacing the rendered asset URL |
| `checksum_url` | absolute http(s) URL of a checksum listing |
| `checksum_algorithm` | `md5`, `sha256`, or `sha3-256`, replacing the asset's algorithm |
| `checksum` | published checksum, validated against the effective algorithm |

Polka renders the matching `download.assets` entry with the returned values, then downloads, verifies, caches, and extracts it as it does for any manifest download.

## `validate(env)`

`env` is read-only:

| Member | Value |
| --- | --- |
| `env.version` | the tool's configured version label |
| `env.tool_version(id)` | any tool's configured version label, or `""` |
| `env.setting(key)` | a setting declared in this manifest's `settings`, typed by its schema: `port` → int, `string` → string, `bool` → bool; unset values read as `0`, `""`, or `False`; undeclared keys are an error |

The hook returns `None`. Report invalid configuration with `fail(msg)`.

## Errors

`fail(msg)` reports `msg` to the user verbatim. Use it for expected failures such as "no matching release found" or invalid settings. Any other error, such as a type error, a missing key, or an exhausted step budget, is treated as a bug in the script and reported with the tool, the script path, and a Starlark backtrace. HTTP failures in `resolve` are reported like `fail`.

## Modules

All hooks can use:

| Module | Functions |
| --- | --- |
| `re` | `find_all(pattern, s)` → list of `[match, group1, ...]`; `search(pattern, s)` → first such list or `None`. Patterns use Go RE2 syntax. |
| `json` | `decode(s)`, `encode(v)`, `indent(s)` |
| `url` | `resolve(base, ref)` → absolute URL |
| `html` | `unescape(s)` |
| `version` | `compare(a, b)` → `-1`, `0`, or `1`; `matches(v, requested)` → `v == requested` or `v` extends `requested` with more dot segments; `best(versions, requested)` → newest matching version or `None` |

Only `resolve` can use:

| Module | Functions |
| --- | --- |
| `http` | `get(url)` → body text; `get_json(url)` → decoded JSON. GET over http(s) only, with Polka's retry behavior and an 8 MiB response limit. |

## Sandbox

Scripts cannot `load()` other files and have no filesystem, environment, or process access. Network access exists only through `http`, and only during `resolve`. Each module load and each hook call is limited to 5,000,000 Starlark steps. `print` output is discarded. Built-in scripts compile at most once per process, on first use, so command dispatch never parses Starlark.

Loading third-party scripts from disk still needs a separate design covering trust, configuration, per-manifest host allowlists, and versioning.

## Testing

`TestBuiltinToolScriptsLoad` compiles every embedded script and fails on scripts no manifest references. Test a resolve hook against an `httptest` server by overriding its variable with `loadManifestWithScriptVar` and calling `resolveManifestRequest`. Use `fstest.MapFS` with `loadManifest` for self-contained fixtures.
