# Polka Architecture

Polka is a Go CLI for managing project-local PHP development environments. The project is organized around a small CLI layer, a backend state layer, shared config types, service runtime orchestration, a higher-level plugin registry, a tool package that knows how to install and dispatch managed tools, and a sandboxed Starlark runtime for tool scripts.

## Package Map

```text
.
|-- main.go
|-- cli/
|-- backend/
|-- config/
|-- plugins/
|-- script/
|-- service/
`-- tools/
```

### `cli`

The `cli` package owns command parsing, user-facing command output, and command-specific workflows. It uses Cobra to expose commands such as `init`, `new`, `install`, `build`, `serve`, `stop`, `exec`, `sh`, `db`, and `status`.

The CLI should stay thin. It resolves flags and arguments, asks `backend.Store` for stateful operations, and handles process execution or runtime command flows where those are command-specific.

### `backend`

The `backend` package owns Polka project state and orchestration:

- locating the project and `polka.yaml`, either by searching upward from the working directory or from the `-C/--dir` project directory, and resolving the state directory from the config's `root` key
- reading, normalizing, and writing config
- managing `.polka/envs`, `.polka/bin`, `.polka/run`, secrets, and logs
- installing tools from the global cache into the project
- syncing dispatch shims for the active environment
- resolving active tool executable paths
- exposing project paths and active registry data used by command/runtime layers

`backend.Store` is the main entry point for persistent project operations. It delegates tool-specific behavior to `tools.Registry` and `tools.Downloader`, and exposes the active tool registry for service runtime orchestration.

`backend/aliases.go` preserves older backend-facing API names such as `backend.Environment`, `backend.ToolRegistry`, and `backend.HTTPToolDownloader` by aliasing the newer `config` and `tools` types.

### `config`

The `config` package contains shared YAML schema types and normalization helpers:

- `Config`
- `Environment`
- `ProjectFile`
- `EnvironmentFile`
- `ManifestVersion` and `CurrentManifestVersion` (`0.2`) for the root project manifest
- `ToolsConfig` (`map[string]string`)
- `SettingsConfig` (`map[string]map[string]any`)
- `DatabaseConfig`
- read-only service view types (`MailpitConfig`, `PHPMyAdminConfig`, `MeilisearchConfig`, `RedisConfig`, `RabbitMQConfig`, `TraefikConfig`)
- `ServerConfig`
- `WorkerConfig` under the top-level `workers` map
- framework IDs stored as `framework` on project and environment files

This package exists to avoid import cycles. Both `backend` and `tools` can depend on config types without either package importing the other.

`ToolsConfig` stores managed tool version labels by tool ID, and `SettingsConfig` stores versionless per-tool scalar settings by namespace and key. Both maps live directly on `Environment`, `ProjectFile`, and `EnvironmentFile`, so adding a new tool or tool setting does not require modifying `config` structs. Built-in service consumers read projected views such as `Environment.Mailpit()` or `Environment.PHPBuildTools()`, while generic callers use `Environment.ToolVersion(id)` and `Environment.SettingBool`/`SettingInt`/`SettingString`.

### `plugins`

The `plugins` package owns Polka's higher-level built-in plugin registry. It groups installable tool plugins from `tools` with framework plugins such as `cakephp`, `codeigniter`, `drupal`, `wordpress`, `laravel`, and `symfony`.

Framework plugins provide config defaults and optional hooks for framework-common PHP extensions, runtime environment variables, OPcache directives, post-Composer secret file generation, and nginx config generation. Database driver extensions belong to the configured database tool rather than the framework. Built-in frameworks are defined entirely in embedded `plugins/manifests/*.yaml` files, `plugins/manifests/templates/*` assets, and `plugins/manifests/scripts/*.star` scripts. The loader enumerates manifests automatically, validates structured value bindings and references, and supplies shared detection, formatting, and file actions. Immutable built-in declarations are parsed once per process. Adding a framework requires no framework-specific Go implementation or registry entry. See [framework-manifests.md](framework-manifests.md) for the authoring schema. Framework init is config-only, and nginx config generation uses the generic front-controller config unless a manifest declares an `nginx_config` script hook.

A `post_composer` script hook returns the same file actions a manifest can declare under `post-composer.actions`. Polka validates the whole plan, then runs it through the same action code, so scripts add logic without adding a second way to write files. Hook contexts never contain database credentials: scripts reference them through structured value bindings that Go resolves after the hook returns. Scripts compile on first use and only these two hooks reach them, so resolving runtime variables and validating config, which happen on every dispatched command, never parse Starlark for a framework. Only a Composer `install`, `update`, or `create-project` does.

`FrameworkPlugin.Detection()` returns isolated detection metadata. Detection uses the active registry for package prefixes, substrings, and distinctive marker files, with declared priority and stable ID tie-breaking. `RuntimeEnv` returns a values map and an error so invalid runtime bindings reach the caller. Post-Composer actions patch dotenv assignments, returned PHP arrays, and PHP constants; they can also render managed files and maintain guarded PHP includes. PHP editing preserves unrelated source and refuses unsupported structures without replacing the affected file. Application PHP is never executed by these editors.

Existing root `polka.yaml` files are validated against the exact `0.2` version before environment decoding, including direct named-environment reads and writes. Obsolete or missing versions require the user to delete and regenerate the file manually. Named environment and internal tool files remain unversioned; cache and runtime state retain their independent integer schema versions.

Project presets are a third plugin kind for non-framework project shapes. They contribute a starting environment plus init-time scaffold files, with no runtime hooks and no marker in `polka.yaml`. Their manifests and templates live under `plugins/manifests/presets/*.yaml` and `plugins/manifests/presets/templates/*`. The built-in `php-extension` preset uses this mechanism to create a PIE-compatible Composer manifest without treating native extension projects as web frameworks.

### `script`

The `script` package is the sandboxed Starlark runtime behind tool and framework scripts. It loads a script from an `fs.FS` and freezes its globals. Each hook call runs on a fresh thread with a step budget. It provides generic modules (`re`, `json`, `url`, `html`), a `fail` builtin whose `*script.FailError` separates user-facing failures from script bugs, and helpers both plugin kinds share for checking a script's public functions against its hooks, classifying hook errors, and reading files through an `os.Root`. It knows nothing about Polka plugins: `tools` and `plugins` add their domain modules (`tools` adds `http` and `version`, and `plugins` reuses `version`) and decide which functions are hooks.

### `service`

The `service` package owns long-running managed services:

- managed MySQL, MariaDB, and PostgreSQL server lifecycle, credentials, state, and data paths
- Mailpit server lifecycle, state, ports, and logs
- phpMyAdmin service lifecycle, UI endpoint helpers, state, and managed MySQL/MariaDB storage bootstrap
- background worker lifecycle for the environment's `workers` config: per-replica process launch, PID state, log files, and reconcile-on-config-change behavior (workers start last and stop first relative to the other managed services); worker launch and stop failures are reported through the warning callback and never fail serve or stop
- service matching against the active `tools.Registry`

Automatic service startup skips a configured service when its matching managed tool plugin is not registered, and reports that through the caller's warning callback. Explicit service commands still fail when their required tool cannot be resolved.

### `tools`

The `tools` package owns managed tool behavior:

- tool IDs such as `PHP`, `PHPZTS`, `FrankenPHP`, `Composer`, `PIE`, `NodeJS`, `Mago`, `Nginx`, `Apache`, `Mailpit`, `Meilisearch`, `Redis`, `Traefik`, `PHPMyAdmin`, `MySQL`, `MariaDB`, `PostgreSQL`, and `SQLite`
- tool plugin interfaces and registry
- embedded YAML manifests for built-in plugin metadata
- PHP extension dependencies, settings schemas (`settings`, `settings-from`), mutual-exclusion peers (`exclusive-with`), version range constraints (`version-constraint`), tool dependencies (`dependencies`), and generated command wrappers (`wrappers`) declared by tool manifests
- install candidate paths and automatic Unix executable permission bits (`0755`) for installed binaries and generated wrappers
- dispatch command mappings (a command without an explicit dispatch candidate falls back to the tool's install candidates)
- declarative download resolution (`download.github` in `releases` or `tag` mode, and `download.release-index` with `{requested}` URL templates), built-in version template placeholders (`{version}`, `{requested}`, `{tag}`, `{major}`, `{minor}`, `{major_minor}`), required checksum verification (`md5`, `sha256`, `sha3-256`, and `embedded-commit-id`), cache metadata, and archive extraction
- nested archive payloads (`unwrap` on a download asset)
- embedded Starlark tool scripts (`script` in the manifest, `tools/manifests/scripts/*.star`, with shared code in `scripts/lib/`) implementing `resolve` hooks for non-standard version discovery (sqlite, apache, php, php-zts, php-devel), `validate` hooks for cross-field rules (rabbitmq), `dependencies` hooks for conditional dependencies (php, php-zts), and `post_install` hooks that generate or patch files in the install directory (phpmyadmin, php-devel); see [tool-scripts.md](tool-scripts.md)
- optional per-tool Go hooks, listed in `goToolHooks`, for work a script plan cannot describe: php and php-zts validation (it runs on every dispatch) and php, php-zts, and frankenphp post-install (it runs the installed binary and host tools)
- tool discovery: every `tools/manifests/*.yaml` becomes a plugin, ordered by `dispatch-priority` and then ID, which decides the provider of a command several tools share
- PHP extension post-install config generation
- compiling tools from source: declarative `build` manifest recipes (`from` shares a recipe between manifests), the `build_sources` script hook that locates sources, and the engine in `tools/build*.go` that checks the host, verifies sources, compiles, packages, and verifies the payload

The current plugin system is internal and compile-time only. Built-in tool metadata and scripts live in `tools/manifests/`, built-in framework metadata and scripts live in `plugins/manifests/`, and both are embedded into the binary; Polka does not load third-party plugins from disk or from `polka.yaml`. The manifest parsers accept bytes and the tool manifest loader (`loadManifest`) reads manifests and their scripts through an `fs.FS`, so future external loading can reuse the schemas and the sandboxed script runtime, but that loading behavior is intentionally not implemented yet.

## Tool Install Flow

`polka install [tool:version] [--env <environment>] [--force]` follows this flow:

1. `cli` resolves the requested environment name from `--env`, the active environment, or `default`.
2. For an explicit `tool:version`, `cli` calls `backend.Store.InstallToolWithProgress`; otherwise it calls `backend.Store.InstallWithProgress`.
3. `backend.Store` loads and normalizes `polka.yaml` for the default environment or `polka.<name>.yaml` for named environments, then validates the install request(s). It builds the effective PHP extension set from framework-common requirements, every configured tool's manifest requirements, and explicit environment overrides in that order.
4. For each requested tool, `backend.Store` first consults the project-local install state in `.polka/envs/installed.json`. Without `--force`, a tool recorded there whose installed executable still resolves on disk skips download and extraction and is reported as unchanged; its post-install hook still runs so configuration changes (PHP extensions, memory limit, OPcache) are applied. Explicit `tool:version` installs always reinstall.
5. Otherwise, `backend.Store` checks the global cache metadata and cached payload checksum.
6. If the cache is missing or invalid, `tools.HTTPDownloader` invokes the matching plugin download hook. A tool with a build recipe and no download for the platform returns `tools.BuildRequiredError` before any network request. See [File transfers](#file-transfers).
7. `backend.Store` installs from the cached payload into `.polka/envs/<tool>/<version>`; archive payloads are extracted on demand, while single-file payloads such as PHARs are placed at their expected install path.
8. The tool plugin may run a post-install hook using `tools.InstallContext`.
9. `backend.Store` records each fully installed tool version in `.polka/envs/installed.json` (including tools that succeeded before another tool's failure aborted the run) and returns install results to the CLI.

### File transfers

`tools/ranged_download.go` transfers each artifact, including build sources. Tools in one dependency layer already install concurrently; this adds concurrency within a single file:

- The first request is a GET for the first 8 MiB. There is no HEAD request, so a server that ignores `Range` answers `200` and that response body is used as a normal single-stream download. Any answer that may be a rejection of `Range` (a `206` without a validator or with an inexact range, `416`, a WAF's `403`) is discarded in favor of a plain GET.
- A valid `206` starts up to four workers per file (`POLKA_DOWNLOAD_CONNECTIONS`, where `1` disables `Range`) writing fixed-size chunks at their offsets. Each chunk request uses the original URL, so expiring signed redirects are issued again, and must return the exact range and the same strong ETag or `Last-Modified`.
- File transfers have no total time limit, on either path, because a total limit fails large downloads on slow links while data is still arriving. Instead, each request is cancelled if no data arrives for two minutes. Metadata requests such as release indexes and checksums keep the client's 10-minute limit.
- A chunk is retried on network errors and short reads, and on those stalls. A single stream gets retries only until its response headers arrive, so a stall in its body fails the download. Any failure that retrying cannot fix, or exhausted retries, restarts the file once as a plain GET after every worker has stopped. Local write errors such as a full disk fail immediately.
- A file's first worker needs no budget, so every tool always makes progress. Additional workers share a process-wide pool of eight connections, so a large layer cannot open four connections per tool to one host.
- Default clients use an HTTP/1.1-only transport, because HTTP/2 would multiplex every range over one TCP connection.

Checksum verification runs on the assembled file, as it does for a single stream. Byte counts reach the CLI as `InstallProgressTransfer` events, delivered from one goroutine per file so they stay ordered, and they are separate from stage events so that consumers printing a line per stage are unaffected. Internal tool provisioning does not report byte counts.

For an explicit `tool:version`, the new version is written to the environment config only after the install succeeds, so a failed resolve, download, or install leaves the previous configuration untouched.

The global cache and project-local install layouts are intentionally separate. The cache stores reusable downloaded payloads under per-tool metadata; `.polka/envs` stores project-local installs selected by the active environment config, alongside the `installed.json` install state (deleting `.polka/envs` resets both together). For user-facing managed tool behavior, command shims, PHP runtime generation, and platform support, see [tools.md](tools.md).

After the tool installs, `backend.Store` provisions any PIE-managed `vendor/name` entries under `php-extensions` that the environment's installed PHP does not yet load, by running the internal PIE against the project PHP (`--with-php-path`, `--skip-enable-extension`). The generated `php.ini` loads those modules, so fresh checkouts reproduce PIE-managed extensions.

As discouraged legacy compatibility, the backend also provisions `pecl-extensions` after PIE. It resolves official PECL metadata, installs matching Windows DLLs or builds Linux sources with target-matched PHP development tools, tracks installed artifacts beside the PHP runtime, and performs a final `php.ini` synchronization. PIE remains the preferred provider.

Finally, on Windows environments with `settings.php.extension-sdk` enabled, `backend/msvc.go` locates a host MSVC toolchain through `vswhere.exe`, captures the variables `vcvarsall.bat` adds, and stores them in `.polka/envs/msvc/<environment>.json`. A missing toolchain is reported through `InstallOptions.Warn` rather than failing the install. `cli` applies the capture when composing `sh`, `exec`, and worker environments (`Store.MSVCToolchain`), before project env files so user values win.

## Tool Build Flow

`polka build <tool:version>` needs no project config, so `cli` uses `StoreForWorkingDirectory`. `backend.Store.BuildTool` delegates to `Store.Builder` (a test seam like `Store.Downloader`), which defaults to `tools.BuildTool`:

1. Reject an invalid request, and return early when the cache already holds the label. Different `--configure-arg` values are an error rather than a reuse.
2. Check host commands and libraries, reporting everything missing together.
3. Take a `flock` per tool and label, because a compile far outlasts the cache metadata lock's staleness window. Check the cache again in case another build just finished.
4. Run the `build_sources` hook, then download and verify each source. A source can only skip verification by declaring `unverified`, which is reported to the user.
5. Extract sources with their modification times preserved, then run each stage's configure, make, and install, with output going only to the log. The compiled prefix is a path that never exists at runtime, and installs go through a destination directory.
6. Apply the recipe's packaging, including rewriting `php-config` and `phpize` to find their install from their own location, and write a tar.gz. Absolute symlinks into the prefix become relative, and any that leave the payload fail the build.
7. Extract and flatten the payload elsewhere and verify it from an unrelated directory, as install would use it.
8. Publish it with `tools.CacheBuiltPayload` under the literal label, recording how it was built. A replacing build clears that label's internal install record.

The build directory is removed on every outcome; the log stays. From here the normal install flow applies, because the payload is an ordinary cache entry.

`polka install --build` runs a serial pass (`Store.buildMissingTools`) before the parallel install layers. It compiles each request that would need a payload, has none cached, and has a recipe, plus the internal PHP when the environment has PIE extensions. The install goroutines then only see cache hits. Recipes use host toolchains rather than other managed tools, so layer order does not matter.

## Internal Tools

Internal tools are consumed by Polka commands rather than exposed to projects. `backend/internal_tools.go` installs them through the same cache pipeline into a machine-global tools directory (`POLKA_TOOLS_DIR`, else `<POLKA_CACHE_DIR>/internal-tools`, else `<user cache dir>/polka/tools`), which acts as the `EnvsDir` of the reserved `_internal` environment:

- `<tools dir>/polka._internal.yaml` records internal tool versions (seeded from the shared default tool versions, user-overridable).
- `<tools dir>/installed.json` reuses the standard install state; `Store.EnsureInternalTool` skips work when the recorded install still resolves on disk, and serializes concurrent cross-project installs with a per-tool lock file.
- Internal PHP installs run the php post-install hook with a synthetic environment enabling a curated broad set of extensions (`tools.InternalPHPExtensions`) — filtered to those whose module file exists in the install's `ext/` directory, always including the TLS baseline (`curl`, `openssl`, `mbstring`, `zip`) — so composer create-project scaffolders and PIE cover most use cases and can reach registries over HTTPS.

Manifests may declare `internal-only: true`: such tools report no version for project environments, expose no dispatch or shim commands, and reject `tools.<id>` project config with a "managed internally" validation error. `polka create-project` uses internal PHP + Composer to download a scaffold with `--no-install --no-scripts` before any project config exists. It then initializes the detected framework and installs project tools. Framework configuration hooks run before building Composer's environment so managed credentials are present in framework files and runtime variables; the managed database starts before Composer scripts unless `--no-scripts` was requested. Composer's `create-project` resumes without a package argument in the scaffold directory. This preserves Composer's root-package, dependency and project hooks in their normal order, using project PHP and the managed shell environment. Framework configuration hooks run again afterward in case Composer rewrote files. Packages without configured PHP or Composer fall back to the internal hosts. `polka ext` and the install pipeline use internal PHP + PIE (`backend/pie.go`) to manage PHP extensions for project runtimes. Dependency-only examples include Erlang for RabbitMQ and the PHP devel/SDK payloads used by native extension builds.

## Dispatch Flow

Managed command shims in `.polka/bin` call back into Polka:

```text
.polka/bin/php
`-- polka -C <project> dispatch php ...
    `-- backend.Store.ResolveTool("php")
     `-- tools.Registry.ResolveDispatchRequestForEnvironment("php", environment)
```

Each shim stores the project directory relative to its own location (absolute only when no relative path exists, such as a state root on another Windows volume), so it works wherever `root` places the state directory and survives moving the whole project. Shims export `POLKA_TOOL_PROJECT_DIR` and `POLKA_TOOL_DISPATCHER`; a shim that inherits them uses them instead, so copies of a shim outside the state directory, such as the ones npm creates, still dispatch to the project that launched them. `-C` selects the project without changing the working directory, so dispatched tools run where they were called.

Dispatch resolution uses the active environment recorded in `.polka/run/current`, or `default` from `polka.yaml` when no local override is selected. It maps command names to the configured provider, reads that environment's definition from `polka.yaml` or `polka.<name>.yaml`, and locates the installed executable under `.polka/envs`. Tool-specific command mappings are declared by manifests and exposed through `tools.Registry`.

## Runtime Services

Runtime services are split between `service` and `cli`:

- PHP built-in server, nginx, Apache, and FrankenPHP serving remain command/runtime concerns in `cli`.
- Managed database lifecycle is in `service`.
- Mailpit startup, shutdown, and status are in `service`, with CLI adapters for command output and test hooks.
- Meilisearch startup, shutdown, and status are in `service`, with CLI adapters for command output and test hooks.
- Redis startup, shutdown, and status are in `service`, with CLI adapters for command output and test hooks.
- Traefik startup, shutdown, and status are in `service`, with CLI adapters for command output and test hooks.
- Background worker startup, shutdown, and status are in `service`; the CLI injects the managed shell environment and managed-tool target resolution so worker commands such as `php artisan queue:work` run against the real installed binaries.
- phpMyAdmin startup, shutdown, status, and managed MySQL/MariaDB storage import are in `service`; CLI supplies the PHP/nginx/Apache/FrankenPHP web runtime callbacks.
- Certificates remain CLI-managed assets and are passed to services through callback adapters.
- The shell command composes environment variables and `PATH` behavior around the active environment.

The database tool plugins install and dispatch database clients, while database server lifecycle logic lives in `service`. PostgreSQL uses `initdb`, `postgres`, `createdb`, `pg_ctl`, `psql`, and `pg_dump`, with client authentication supplied through a project-local `.pgpass` file.

## Dependency Rules

Keep package dependencies moving in this direction:

```text
cli -> backend -> plugins -> tools -> config
cli -> backend -> tools -> config
cli -> backend -> config
cli -> service -> tools -> config
backend -> config
backend -> service -> tools -> config
tools -> script
plugins -> script
```

Avoid these dependencies:

- `tools -> backend`
- `plugins -> backend`
- `config -> backend`
- `config -> tools`
- `service -> backend`
- `script -> any other polka package`

Plugin hooks receive `tools.InstallContext`, which contains layout paths and install results rather than a `backend.Store`. This keeps the tool package independent from backend orchestration.

## Adding A Managed Tool

To add a new managed tool:

1. Create `tools/manifests/<id>.yaml`, with an `id` matching the file name. Discovery registers it automatically. Add a tool ID in `tools/ids.go` only as a convenience constant for built-in Go callers.
2. If the tool shares a dispatch command with another tool, set `dispatch-priority` so the provider is a deliberate choice. Higher values win when no configured tool decides it.
3. Declare any versionless settings (`settings: <key>: bool|port|string`), shared settings namespace (`settings-from`), mutually exclusive tool peers (`exclusive-with`), version bounds (`version-constraint`), install-time dependencies (`dependencies`), or generated command wrappers (`wrappers`) in `tools/manifests/<id>.yaml`; version lookup from `config.Environment.ToolVersion(id)`, version-constraint checks, port range checks, and `<id> configuration requires version` checks are handled automatically by the manifest plugin and `tools.Registry`.
4. If the tool has cross-field invariants beyond scalar setting types and version bounds (for example distinct port pairs or single-line credentials), implement a `validate` hook in a manifest script (`script.hooks: [validate]`). Use a Go `Validate` hook only when the rule needs data a script cannot reach.
5. Declare `install-candidates` in the manifest. Add `dispatch-candidates` only to override commands whose executable differs from the install candidates (for example a multi-binary tool); every dispatch command without an override resolves to the install candidates. Unix executable permission bits (`0755`) are applied automatically during post-install.
6. Declare `download` (`github` or `release-index` plus `assets`) in the manifest so the default manifest downloader handles release resolution, checksum verification, and caching automatically. When upstream discovery needs custom parsing, keep `download.assets` and add a script `resolve` hook that returns the resolved version, template values, and checksum. Declare `unwrap` on an asset whose payload is an archive nested inside the download. Add a custom Go `download` hook only when the payload needs processing neither can describe.
7. For conditional dependencies or generated files, implement a script `dependencies` or `post_install` hook. Add a Go hook to `goToolHooks` in `tools/plugins.go` only when the work needs to run a binary or use host tools. See [tool-scripts.md](tool-scripts.md) for the script contract.
8. Add tests in `tools` for registry, manifest, candidate, and downloader behavior.
9. Add backend or CLI tests only when the new tool changes project state, shims, command output, or runtime behavior.

Do not add runtime plugin loading without a separate design. That would need config schema, trust/security rules, loading behavior, versioning, and error reporting.

## Testing Expectations

Use the package boundary to choose tests:

- `tools`: plugin registry, candidate paths, download asset selection, tool script hooks, archive extraction, post-install hooks, and the build engine (through the `buildCommandRunner` fake, so it runs on every CI platform).
- `script`: sandbox limits, error classification, and generic Starlark modules.
- `plugins`: framework manifest validation, framework script hooks, and generated files, which goldens in `plugins/testdata/post-composer/` pin for every built-in and engine.
- `config`: normalization behavior when shared helpers become complex enough to warrant direct tests.
- `backend`: store behavior, config persistence, install materialization, shim syncing, tool resolution, runtime state, and service orchestration.
- `cli`: command parsing, user-facing output, command selection, and full command workflows.

The default acceptance check is:

```bash
go test ./...
```

Compiling real tools is too slow and host-dependent for that check, so it sits behind a variable. It builds the recipes, serves pages through them, and installs an extension with PIE:

```bash
POLKA_BUILD_INTEGRATION=1 go test ./cli -run TestBuildIntegration -v -timeout 40m
```

`POLKA_BUILD_INTEGRATION_CACHE` reuses a cache between runs, and `POLKA_BUILD_INTEGRATION_PHP_ARGS` passes configure options such as `--without-tidy`.
