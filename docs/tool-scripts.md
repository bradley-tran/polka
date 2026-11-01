# Tool scripts

A tool manifest (`tools/manifests/<id>.yaml`) can delegate logic the YAML schema cannot express to a sandboxed [Starlark](https://github.com/google/starlark-go) script. Scripts are embedded with the manifests under `tools/manifests/scripts/*.star` and replace per-tool Go hooks. The loader reads manifests and scripts through an `fs.FS`, but Polka still does not load plugins from disk.

Scripts follow one rule: **scripts plan, Go acts.** A hook returns data, and Polka validates it and performs every download, checksum, and file write itself.

Scripts can implement four hooks:

| Hook | Replaces | Runs |
| --- | --- | --- |
| `resolve(ctx)` | a Go `download` hook that scrapes a release listing | when a tool version is downloaded into the cache |
| `validate(env)` | a Go `validate` hook | whenever an environment is validated, only if the tool has a version or settings |
| `dependencies(env)` | a Go `dependencies` hook, or static `dependencies` in the manifest | when install requests are computed, only if the tool has a version |
| `post_install(ctx)` | a Go `postInstall` hook | after every install, including installs skipped as already up to date, only if the install directory exists |

Framework plugins remain declarative YAML. A Go hook is still right for work a plan cannot describe, such as running the installed binary or using host tools. `goToolHooks` in `tools/plugins.go` lists the tools that need one.

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
- `hooks` lists the hooks the script implements, without duplicates. Declaring hooks keeps manifest validation static, because scripts are only compiled when a hook first runs.
- `vars` is an optional string map handed to hooks as `ctx.vars` or `env.vars`. Keys are lowercased. Put upstream URLs here so the hosts a script contacts stay visible in YAML and tests can point them at local servers.
- A `resolve` hook requires `download.assets` and cannot be combined with `download.github` or `download.release-index`.
- A `dependencies` hook cannot be combined with the manifest's `dependencies`.
- A tool cannot implement the same hook in both Go and Starlark.

Tool manifests parse strictly, so an unknown key is an error.

## Script structure

```python
load("shared.star", "helper")

_PREFIXES = {"windows-amd64": "sqlite-tools-win-x64-"}

def _clean(text):
    return text.strip()

def resolve(ctx):
    ...
```

Each declared hook must be a top-level function taking exactly one parameter. Other top-level functions must start with `_`, so a misspelled hook name fails instead of being silently ignored. Constants may be public.

Polka uses the conservative Starlark dialect: no `while`, no recursion, no top-level `if`/`for`, no global reassignment, and no `set`. Globals are frozen after the script loads, so hooks cannot keep state between calls.

### Shared code

`load()` reads library scripts from `tools/manifests/scripts/lib/*.star`. Names are relative to that directory and cannot leave it, so a script cannot load another hook script. Library scripts export public names, are not bound to any manifest, and follow the same dialect. Share code this way when scripts must agree, as the php and php-devel scripts do when selecting a build.

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
| `values` | extra `{placeholder}` values for the asset templates; names use letters, numbers, and underscores and cannot override `version`, `requested`, `tag`, `major`, `minor`, or `major_minor`; a rendered file name cannot contain path separators |
| `url` | absolute http(s) URL replacing the rendered asset URL |
| `checksum_url` | absolute http(s) URL of a checksum listing |
| `checksum_algorithm` | `md5`, `sha256`, or `sha3-256`, replacing the asset's algorithm |
| `checksum` | published checksum, validated against the effective algorithm |

Polka renders the matching `download.assets` entry with the returned values, then downloads, verifies, caches, and extracts it as it does for any manifest download.

### Nested archives

An asset can declare that the real payload is an archive inside the download, with or without a script:

```yaml
linux-amd64:
  filename: outer-{version}.jar
  url: https://example.com/outer-{version}.jar
  archive-format: zip              # format of the download
  unwrap:
    path: payload.txz              # file inside the download
    filename: tool-{version}.tar.xz
    archive-format: tar.xz         # format of the cached payload
```

Any published checksum applies to the outer download. The cache then records the nested file's own checksum.

## `validate(env)` and `dependencies(env)`

`env` is read-only:

| Member | Value |
| --- | --- |
| `env.version` | the tool's configured version label |
| `env.os`, `env.arch`, `env.platform` | the host platform |
| `env.vars` | the manifest's `script.vars` |
| `env.tool_version(id)` | any tool's configured version label, or `""` |
| `env.setting(key)` | a setting declared in this manifest's `settings`, or in the `settings-from` manifest's, typed by its schema: `port` → int, `string` → string, `bool` → bool; unset values read as `0`, `""`, or `False`; undeclared keys are an error |

`validate` returns `None` and reports invalid configuration with `fail(msg)`.

`dependencies` returns a list of `{"tool": ..., "version": ...}` dicts, which may be empty. Unknown keys are rejected, versions must be valid labels, and a tool cannot depend on itself.

## `post_install(ctx)`

`ctx` is read-only and deliberately narrow:

| Member | Value |
| --- | --- |
| `ctx.tool`, `ctx.version` | tool ID and installed version label |
| `ctx.os`, `ctx.arch`, `ctx.platform` | the host platform |
| `ctx.vars` | the manifest's `script.vars` |
| `ctx.environment` | environment name, which may be empty |
| `ctx.root_dir` | absolute project state directory |
| `ctx.database` | `None`, or a value with `engine` (lowercase) and `port`; never credentials |
| `ctx.read_text(path)` | a file from the install directory as a string, or `None` if it does not exist; 8 MiB limit |

The hook returns a dict. Unknown keys are rejected, and `None` values count as unset:

| Key | Meaning |
| --- | --- |
| `write` | map of path to file content; parent directories are created, and files use mode `0644` |
| `mkdir` | list of directories to create |

Paths are slash-separated and relative to the install directory. Polka validates the entire plan before changing anything, so an invalid plan writes nothing. Reads and writes that resolve outside the install directory, including through symlinks, are refused.

The hook also runs when an install is skipped, so it must be idempotent. Return only what needs to change, or regenerate the file in full.

## Errors

`fail(msg)` reports `msg` to the user verbatim. Use it for expected failures such as "no matching release found" or invalid settings. Any other error, such as a type error, a missing key, or an exhausted step budget, is treated as a bug in the script and reported with the tool, the script path, and a Starlark backtrace. HTTP failures, malformed `http.get_json` responses, and failures reading install files are reported like `fail`.

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

Only `post_install` can use:

| Module | Functions |
| --- | --- |
| `path` | `join(*parts)` → joined path using the host's separators, for absolute paths that go into generated configuration |
| `crypto` | `random_base64(n)` → `n` random bytes (1 to 1024) as unpadded base64, for generated secrets |

## Sandbox

Scripts have no general filesystem, environment, or process access. The only file access is `ctx.read_text` and a returned plan, both confined to the install directory. Network access exists only through `http`, and only during `resolve`. Each module load, library load, and hook call is limited to 5,000,000 Starlark steps. `print` output is discarded.

Built-in scripts compile at most once per process, on first use. Command dispatch validates the active environment, so it compiles only the scripts of configured tools that implement `validate`. Keep `validate` hooks off tools that run constantly: php stays in Go for this reason, and a test fails if php dispatch compiles Starlark.

Loading third-party scripts from disk still needs a separate design covering trust, configuration, per-manifest host allowlists, and versioning.

## Testing

`TestBuiltinToolScriptsLoad` compiles every embedded script and fails on scripts no manifest references and on libraries no script loads. Test a resolve hook against an `httptest` server by overriding its variable with `loadManifestWithScriptVar` and calling `resolveManifestRequest`, then `downloadManifestAsset` to cover the full path. Test `post_install` through `PostInstall` with a temporary install directory. Use `fstest.MapFS` with `loadManifest` for self-contained fixtures.

Files under `tools/testdata/` were captured from the Go code that the phpMyAdmin and php-devel scripts replaced, and keep their exact line endings through a `-text` attribute. Treat a difference as a behavior change.
