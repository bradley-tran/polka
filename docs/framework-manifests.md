# Framework manifests

Framework integrations are embedded in `plugins/manifests/*.yaml`; assets live in `plugins/manifests/templates/*`, and optional [scripts](#scripts) live in `plugins/manifests/scripts/*.star`. Adding a manifest registers the framework automatically on the next build. Project files select its `id` through `framework`. There is no runtime loading from disk.

The project schema version is `0.2` in `polka.yaml`. Embedded framework declarations do not have a separate version field. Legacy `runtime-env.database` and `post-composer.strategy` declarations are rejected.

## Example

```yaml
id: acme
defaults:
  docroot: public
  tools:
    php: "8.4"
    composer: "2.8"
    mariadb: "11.8"
  database:
    engine: mariadb
    port: 3306
database-engines: [mysql, mariadb, postgresql]
php-extensions: [mbstring, openssl]
detection:
  package-prefixes: [acme/]
  markers: [acme.lock]
  priority: 10
app-root:
  public-dir: public
  markers: [acme.lock, composer.json]
values:
  driver:
    by-engine: {mysql: mysql, mariadb: mysql, postgresql: pgsql}
runtime-env:
  requires-database: true
  values:
    DB_CONNECTION: {from: values.driver}
    DB_PASSWORD: {from: database.password}
post-composer:
  requires-database: true
  actions:
    - type: dotenv
      path: .env
      fallback: .env.example
      values:
        DB_CONNECTION: {from: runtime-env.DB_CONNECTION}
        DB_PASSWORD: {from: runtime-env.DB_PASSWORD}
      order: [DB_CONNECTION, DB_PASSWORD]
```

Existing metadata fields `defaults`, `php-extensions`, `database-engines`, and `opcache-config` retain their meanings. Defaults require a docroot; `defaults.framework`, if specified, must equal `id`. Framework database driver extensions still come from the configured database tool.

## Detection and application roots

`detection` accepts `package-prefixes`, `package-substrings`, `markers`, and integer `priority` (default `0`). Package names are compared case-insensitively with a trailing Composer version constraint removed. All prefix matches precede substring matches; package matches precede file markers. Within each category, higher priority wins, followed by ascending framework ID. Marker lists use any-match semantics and require regular files. A bare `composer.json` cannot be a detection marker. Filesystem errors other than missing files are reported.

`app-root.public-dir` and `app-root.markers` locate the framework within a project after Composer. Candidate order is the create-project target, the root inferred from the configured docroot, the working directory, and the project directory. The first candidate matching an app-root marker wins; otherwise the first candidate is used. Unlike detection, root hints may include `composer.json`.

## Structured values

Every value is a YAML scalar (string, number, boolean, or null) or one expression:

| Form | Meaning |
|---|---|
| `{from: database.host}` | Resolve a context field or named value |
| `{from: database.version, default: "11.8"}` | Substitute a default when the result is empty or null |
| `{by-engine: {mysql: value, mariadb: value, postgresql: value}}` | Select by the normalized database engine |
| `{format: {kind: string, value: binding}}` | Convert a scalar to text |
| `{format: {kind: host-port, host: binding, port: binding, omit-port: "3306"}}` | Compose a host and optional port, with IPv6 handling |
| `{format: {kind: version, value: binding, strip-prefix: mariadb-, min-parts: 3, prefix: mariadb-}}` | Normalize version components and affixes |

`database` fields are `engine`, `version`, `host`, `port`, `name`, `user`, and `password`, exposed as text. Host defaults to `127.0.0.1`; port defaults to `5432` for PostgreSQL and `3306` otherwise. An absent engine uses the environment engine, then `mysql`. Version uses the environment's explicit database version, then its selected tool version, then credential metadata. Database values require managed credentials. Normalization does not mutate caller data.

`values.<name>` references named values and `runtime-env.<key>` references declared runtime variables. Names retain their exact spelling, including dotted variable names. References and cycles are checked across all branches during loading. A `by-engine` expression must cover every engine in `database-engines`.

The URL formatter has structured components and omits empty query values:

```yaml
format:
  kind: url
  scheme: {from: values.scheme}
  user: {from: database.user}
  password: {from: database.password}
  host: {from: database.host}
  port: {from: database.port}
  path: {from: database.name}
  query:
    charset: {from: values.charset}
    serverVersion: {from: values.server-version}
```

`host`, `scheme`, and `path` bindings are required; other URL components are optional. User information, the database path component, and query values are escaped separately. Ports must be valid TCP ports. The built-in Symfony manifest shows per-engine URL and server-version declarations.

`runtime-env.requires-database` and `post-composer.requires-database` default to `false`. Set them to `true` for database-dependent integrations: their entire block then becomes a no-op without credentials. A database reference without credentials in an unguarded block returns an error. Runtime values are converted to strings.

## File actions

Every action has `type`, `path`, and optional `base` (`app-root`, the default, or `docroot`). Paths use forward slashes and must stay inside the chosen base; absolute, drive-qualified, backslash, traversal, and escaping symlink paths are rejected. Actions run in declaration order.

`fallback` names a file relative to the same base. `fallback-template-file` names an embedded asset. Existing targets take precedence over both. Only a missing file triggers fallback; read failures are returned. PHP editing actions require an embedded fallback if both target and project fallback are absent. A missing dotenv file can start empty; `php-include` can start with `<?php`.

| Type | Additional fields and behavior |
|---|---|
| `dotenv` | `values` maps assignment names to bindings. Optional `order` controls insertions; unlisted keys follow alphabetically. Preserves unrelated entries and escapes quoted values. |
| `php-array` | `updates` is a list of `{path: [Datasources, default], values: {...}, order: [...]}`. Paths locate containers in the returned PHP array. Missing literal branches are created; scalar leaves are updated. |
| `php-constants` | `values` maps constant names to bindings. Optional `order` controls insertion. `insert-before` names an existing constant used as an insertion anchor; otherwise missing constants are inserted before bootstrap includes or the PHP closing tag. |
| `write-file` | Requires `template-file`, `encoding` (`text` or `php-literal`), and bindings under `values`. Replaces `{{name}}` placeholders once. Generated files are managed and replaced on each run. |
| `php-include` | Requires `include`, a path relative to the target's directory, and `marker`, an identifier. Ensures a guarded `__DIR__` include between `// <polka:marker>` and `// </polka:marker>`. Existing managed blocks are refreshed and moved after application settings so installer-written credentials cannot override managed values. |

PHP arrays and constants preserve unrelated source, comments, and line endings. String values are PHP-quoted; booleans, numbers, and null retain their types. Literal short arrays and `array(...)` syntax are supported. Dynamic keys, computed containers, duplicate managed constants, heredocs, and other unsupported structures are refused without replacing the affected file. The editor never executes application PHP.

Targets are staged before replacement. Secret files use mode `0600`; include files use `0644`. Cross-file updates are ordered rather than transactional: an earlier successful action remains applied if a later action fails.

Unknown fields, old selectors, invalid references, cycles, missing template assets, unknown placeholders, conflicting expression/action fields, and duplicate or unknown `order` keys fail during loading. Keep regression tests beside the plugin code and run `go test ./...` after adding or changing a manifest.

## Scripts

A manifest can delegate logic the YAML cannot express to a sandboxed [Starlark](https://github.com/google/starlark-go) script, such as looping over repeated blocks or branching on a tool version or an application file. All built-in frameworks use one for their post-Composer actions.

Framework scripts follow the same rule as [tool scripts](tool-scripts.md): **scripts plan, Go acts.** A hook returns data, and Polka validates it and performs every file write and all escaping itself.

```yaml
script:
  file: scripts/acme.star
  hooks: [post_composer]
post-composer:
  requires-database: true
```

- `file` is a `.star` path relative to the manifest directory. It must exist and stay inside that directory.
- `hooks` lists the hooks the script implements, without duplicates. Each must be a top-level function taking exactly one parameter. Other top-level functions must start with `_`, so a misspelled hook name fails instead of being silently ignored.
- A `post_composer` hook cannot be combined with `post-composer.actions`, and requires `app-root.public-dir`.
- `post-composer.requires-database` still applies: without credentials the hook does not run or compile.

| Hook | Runs |
|---|---|
| `post_composer(ctx)` | after Composer `install`, `update`, or `create-project`; `create-project` runs it twice, so it must be idempotent |
| `nginx_config(ctx)` | when `polka serve` generates an nginx config |

Runtime variables and framework validation have no hooks and stay declarative. Polka evaluates them on every managed command, including each `php` call through a shim, and `TestFrameworkDispatchPathDoesNotCompileScript` keeps Starlark off that path.

Scripts use the dialect, `fail()`, error reporting, step budget, and `re`, `json`, `url`, `html`, and `version` modules described in [tool-scripts.md](tool-scripts.md). `http`, `crypto`, and `path` are not available, so using them is a compile error. `load()` reads libraries from `plugins/manifests/scripts/lib/*.star`; add that pattern to the `go:embed` line in `plugins/manifest.go` along with the first library, because a pattern with no matches does not compile.

### `post_composer(ctx)`

`ctx` is read-only:

| Member | Value |
|---|---|
| `ctx.framework`, `ctx.environment` | framework ID and environment name |
| `ctx.os`, `ctx.arch`, `ctx.platform` | the host platform |
| `ctx.database` | `None`, or a value with `engine`, `version`, `host`, and `port` (an int) |
| `ctx.tool_version(id)` | any tool's configured version label, or `""` |
| `ctx.read_text(path, base="app-root")` | a file under `app-root` or `docroot` as a string, or `None` if it does not exist; 8 MiB limit; reads that resolve outside the base, including through links, are refused |

The hook returns `{"actions": [...]}`, which may be empty. Each action uses exactly the [file action](#file-actions) schema, with the same kebab-case keys, and each value is a [structured value](#structured-values):

```python
_DRIVERS = {"mysql": "mysql", "mariadb": "mysql", "postgresql": "pgsql"}

def post_composer(ctx):
    # A custom layout decides where the configuration goes.
    path = ".env.local" if ctx.read_text("acme.lock") else ".env"
    return {"actions": [{
        "type": "dotenv",
        "path": path,
        "values": {
            "DB_DRIVER": _DRIVERS[ctx.database.engine],
            "DB_USER": {"from": "database.user"},
            "DB_PASSWORD": {"from": "database.password"},
        },
        "order": ["DB_DRIVER", "DB_USER", "DB_PASSWORD"],
    }]}
```

Polka does not hand credentials to scripts. `ctx.database` holds no database name, user, or password, so a script references them with bindings such as `{"from": "database.password"}`, and Polka resolves those after the hook returns. All PHP, dotenv, and URL escaping therefore stays in Go. Use a `format` binding instead of building a connection string from parts.

`ctx.read_text` is the one way a script can still come across a credential. Files under the application root, such as `.env`, can hold values Polka wrote on an earlier run, and Polka's state directory may sit inside that root. Scripts have no network access, so a failure message is the only way out: do not repeat file contents in `fail()`.

Polka validates the entire plan before running its first action, using the checks manifests get at load time, so an invalid plan writes nothing. Bindings may reference `values.*` and `runtime-env.*` entries declared in the manifest. Because these checks run when the hook runs rather than when the manifest loads, a mistake in a branch no test reaches only surfaces at that point.

### `nginx_config(ctx)`

`ctx` is read-only and has `framework`, `environment`, `os`, `arch`, `platform`, `host`, `port`, `docroot`, `index_names`, `try_files_fallback`, `fastcgi_index`, `backend_address`, `tls` (`None`, or a value with `certificate` and `certificate_key`), and `default_config`, the generic config Polka would otherwise write.

The hook returns `None` to keep the generic config, or a non-empty string to replace it. Prefer editing `default_config`, whose paths are already quoted, to building a config from scratch. Polka does not validate the returned text before writing it.

```python
def nginx_config(ctx):
    return ctx.default_config.replace(
        "        location / {",
        "        location /private { deny all; }\n        location / {",
    )
```

### Testing scripts

`TestBuiltinFrameworkScriptsLoad` compiles every embedded script and fails on scripts no manifest references. `TestBuiltinFrameworkEngineMatrix` and `TestBuiltinFrameworkPostComposerGolden` run each built-in on every engine, and from project fallback files. The goldens in `plugins/testdata/post-composer/` pin the exact generated files, so treat a difference as a behavior change, and set `POLKA_UPDATE_TESTDATA=1` to regenerate them after an intended one. Use `fstest.MapFS` with `parseFrameworkManifestWithFiles` for self-contained fixtures.
