# Polka Architecture

Polka is a D (`ldc2` / `dub`) CLI for managing project-local PHP development environments. The project is organized under `source/` around a thin CLI layer (`polka.cli`), a backend state layer (`polka.backend`), shared config types (`polka.config`), service runtime orchestration (`polka.service`), a higher-level plugin registry (`polka.plugins`), managed tool installation/dispatch (`polka.tools`), shared system/format helpers (`polka.support`), and compile-time embedded YAML manifests (`tools/manifests/` and `plugins/manifests/`).

## Package Map

```text
.
|-- dub.sdl
|-- source/
|   |-- app.d
|   `-- polka/
|       |-- cli/
|       |-- backend/
|       |-- config/
|       |-- plugins/
|       |-- service/
|       |-- support/
|       `-- tools/
|-- plugins/manifests/
`-- tools/manifests/
```

### `polka.cli`

The `polka.cli` package (`source/polka/cli/`) owns command parsing, help output, user-facing command output, interactive terminal views, web server adapters, and command-specific workflows. It exposes commands such as `init`, `new`, `install`, `serve`, `stop`, `exec`, `sh`, `db`, and `status`.

The CLI stays thin: `source/app.d` delegates to `polka.cli.run`, which resolves flags and arguments, asks `polka.backend.Store` for stateful operations, and handles process execution or runtime command flows where those are command-specific.

### `polka.backend`

The `polka.backend` package (`source/polka/backend/`) owns Polka project state and orchestration:

- locating the project and `polka.yaml`
- reading, normalizing, and writing config (with YAML comment preservation)
- managing `.polka/envs`, `.polka/bin`, `.polka/run`, secrets, and logs
- installing tools from the global cache into the project
- syncing dispatch shims for the active environment
- resolving active tool executable paths
- exposing project paths and active registry data used by command/runtime layers

`Store` (`source/polka/backend/store.d`) is the main entry point for persistent project operations. It delegates tool-specific behavior to `polka.tools.Registry` and `polka.tools.Downloader`, and exposes the active tool registry for service runtime orchestration.

`source/polka/backend/aliases.d` preserves older backend-facing API names such as `Environment`, `ToolRegistry`, and `HTTPToolDownloader` by aliasing the `polka.config` and `polka.tools` types.

### `polka.config`

The `polka.config` package (`source/polka/config/`) contains shared YAML schema types and normalization helpers:

- `Config`
- `Environment`
- `ProjectFile`
- `EnvironmentFile`
- `ManifestVersion` and `CurrentManifestVersion` (`0.2`) for the root project manifest
- `ToolsConfig` (`string[string]`)
- `SettingsConfig` (`YamlValue[string][string]`)
- `DatabaseConfig`
- read-only service view types (`MailpitConfig`, `PHPMyAdminConfig`, `MeilisearchConfig`, `RedisConfig`, `RabbitMQConfig`, `TraefikConfig`)
- `ServerConfig`
- `WorkerConfig` under the top-level `workers` map
- framework IDs stored as `framework` on project and environment files

This package exists to avoid import cycles. Both `polka.backend` and `polka.tools` depend on config types without either package importing the other.

`ToolsConfig` stores managed tool version labels by tool ID, and `SettingsConfig` stores versionless per-tool scalar settings by namespace and key. Both maps live directly on `Environment`, `ProjectFile`, and `EnvironmentFile`, so adding a new tool or tool setting does not require modifying `polka.config` structs. Built-in service consumers read projected views such as `Environment.mailpit()` or `Environment.phpBuildTools()`, while generic callers use `Environment.toolVersion(id)` and `Environment.settingBool`/`settingInt`/`settingString`.

### `polka.plugins`

The `polka.plugins` package (`source/polka/plugins/`) owns Polka's higher-level built-in plugin registry. It groups installable tool plugins from `polka.tools` with framework plugins such as `cakephp`, `codeigniter`, `drupal`, `wordpress`, `laravel`, and `symfony`.

Framework plugins provide config defaults and optional hooks for framework-common PHP extensions, runtime environment variables, OPcache directives, post-Composer secret file generation, and nginx config generation. Database driver extensions belong to the configured database tool rather than the framework. Built-in frameworks are defined entirely in embedded `plugins/manifests/*.yaml` files and `plugins/manifests/templates/*` assets imported at compile time via `dub.sdl` `stringImportPaths`. The loader enumerates manifests automatically, validates structured value bindings and references, and supplies shared detection, formatting, and file actions. Immutable built-in declarations are parsed once per process. Adding a framework requires no framework-specific D implementation or registry entry. See [framework-manifests.md](framework-manifests.md) for the authoring schema. Framework init is config-only and framework nginx hooks delegate to the generic front-controller config.

`FrameworkPlugin.Detection()` returns isolated detection metadata. Detection uses the active registry for package prefixes, substrings, and distinctive marker files, with declared priority and stable ID tie-breaking. `RuntimeEnv` returns a values map and an error so invalid runtime bindings reach the caller. Post-Composer actions patch dotenv assignments, returned PHP arrays, and PHP constants; they can also render managed files and maintain guarded PHP includes. PHP editing preserves unrelated source and refuses unsupported structures without replacing the affected file. Application PHP is never executed by these editors.

Existing root `polka.yaml` files are validated against the exact `0.2` version before environment decoding, including direct named-environment reads and writes. Obsolete or missing versions require the user to delete and regenerate the file manually. Named environment and internal tool files remain unversioned; cache and runtime state retain their independent integer schema versions.

Project presets are a third plugin kind for non-framework project shapes. They contribute a starting environment plus init-time scaffold files, with no runtime hooks and no marker in `polka.yaml`. Their manifests and templates live under `plugins/manifests/presets/*.yaml` and `plugins/manifests/presets/templates/*`. The built-in `php-extension` preset uses this mechanism to create a PIE-compatible Composer manifest without treating native extension projects as web frameworks.

### `polka.service`

The `polka.service` package (`source/polka/service/`) owns long-running managed services:

- managed MySQL, MariaDB, and PostgreSQL server lifecycle, credentials, state, and data paths
- Mailpit server lifecycle, state, ports, and logs
- phpMyAdmin service lifecycle, UI endpoint helpers, state, and managed MySQL/MariaDB storage bootstrap
- background worker lifecycle for the environment's `workers` config: per-replica process launch, PID state, log files, and reconcile-on-config-change behavior (workers start last and stop first relative to the other managed services); worker launch and stop failures are reported through the warning callback and never fail serve or stop
- service matching against the active `polka.tools.Registry`

Automatic service startup skips a configured service when its matching managed tool plugin is not registered, and reports that through the caller's warning callback. Explicit service commands still fail when their required tool cannot be resolved.

### `polka.tools`

The `polka.tools` package (`source/polka/tools/`) owns managed tool behavior:

- tool IDs such as `PHP`, `PHPZTS`, `FrankenPHP`, `Composer`, `PIE`, `NodeJS`, `Mago`, `Nginx`, `Apache`, `Mailpit`, `Meilisearch`, `Redis`, `Traefik`, `PHPMyAdmin`, `MySQL`, `MariaDB`, `PostgreSQL`, and `SQLite`
- tool plugin interfaces and registry (`source/polka/tools/registry.d`, `source/polka/tools/types.d`)
- compile-time string-imported YAML manifests (`tools/manifests/*.yaml`) for built-in plugin metadata
- PHP extension dependencies, settings schemas (`settings`, `settings-from`), mutual-exclusion peers (`exclusive-with`), version range constraints (`version-constraint`), tool dependencies (`dependencies`), and generated command wrappers (`wrappers`) declared by tool manifests
- install candidate paths and automatic Unix executable permission bits (`0755`) for installed binaries and generated wrappers
- dispatch command mappings (a command without an explicit dispatch candidate falls back to the tool's install candidates)
- declarative download resolution (`download.github` in `releases` or `tag` mode, and `download.release-index` with `{requested}` URL templates), built-in version template placeholders (`{version}`, `{requested}`, `{tag}`, `{major}`, `{minor}`, `{major_minor}`), required checksum verification (`md5`, `sha256`, `sha3-256`, and `embedded-commit-id`), cache metadata, and archive extraction
- optional per-tool D hooks for custom multi-field validation, non-standard download discovery, or custom post-install behavior
- PHP extension post-install config generation

### `polka.support`

The `polka.support` package (`source/polka/support/`) provides shared low-level primitives with zero external DUB dependencies:

- `yaml.d`: `libyaml` parser/emitter, strict unknown-field checks, raw scalar version handling, and comment-preserving serialization
- `archive.d`: `.zip`, `.tar.gz`, and `.tar.xz` (`liblzma`) streaming extraction with traversal/symlink containment checks and EOCD comment verification
- `crypto.d`: OpenSSL `libcrypto` bindings for SHA3-256, RSA-2048 keygen, X.509 v3 CA/server certificates, and AES-256-GCM encryption plus Phobos digests
- `http.d`: `libcurl`-backed `HTTPClient` interface with retry/backoff and mock test doubles
- `xml.d`, `fs.d`, `net.d`: PECL REST XML parsing, cross-platform filesystem/executable helpers, and loopback listener checks

The current plugin system is internal and compile-time only. Built-in tool metadata lives in `tools/manifests/*.yaml`, built-in framework metadata lives in `plugins/manifests/*.yaml`, and both are imported into the binary at compile time (`import(...)`); Polka does not load third-party plugins from disk or from `polka.yaml`. The manifest parsers accept YAML text so future external loading can reuse the schemas, but that loading behavior is intentionally not implemented yet.

## Tool Install Flow

`polka install [tool:version] [--env <environment>] [--force]` follows this flow:

1. `polka.cli` resolves the requested environment name from `--env`, the active environment, or `default`.
2. For an explicit `tool:version`, `polka.cli` calls `Store.installToolWithProgress`; otherwise it calls `Store.installWithProgress`.
3. `polka.backend.Store` loads and normalizes `polka.yaml` for the default environment or `polka.<name>.yaml` for named environments, then validates the install request(s). It builds the effective PHP extension set from framework-common requirements, every configured tool's manifest requirements, and explicit environment overrides in that order.
4. For each requested tool, `Store` first consults the project-local install state in `.polka/envs/installed.json`. Without `--force`, a tool recorded there whose installed executable still resolves on disk skips download and extraction and is reported as unchanged; its post-install hook still runs so configuration changes (PHP extensions, memory limit, OPcache) are applied. Explicit `tool:version` installs always reinstall.
5. Otherwise, `Store` checks the global cache metadata and cached payload checksum.
6. If the cache is missing or invalid, `HTTPToolDownloader` (`polka.tools.downloader`) invokes the matching plugin download hook.
7. `Store` installs from the cached payload into `.polka/envs/<tool>/<version>`; archive payloads are extracted on demand, while single-file payloads such as PHARs are placed at their expected install path.
8. The tool plugin may run a post-install hook using `ToolInstallContext`.
9. `Store` records each fully installed tool version in `.polka/envs/installed.json` (including tools that succeeded before another tool's failure aborted the run) and returns install results to the CLI.

For an explicit `tool:version`, the new version is written to the environment config only after the install succeeds, so a failed resolve, download, or install leaves the previous configuration untouched.

The global cache and project-local install layouts are intentionally separate. The cache stores reusable downloaded payloads under per-tool metadata; `.polka/envs` stores project-local installs selected by the active environment config, alongside the `installed.json` install state (deleting `.polka/envs` resets both together). For user-facing managed tool behavior, command shims, PHP runtime generation, and platform support, see [tools.md](tools.md).

After the tool installs, `Store` provisions any PIE-managed `vendor/name` entries under `php-extensions` that the environment's installed PHP does not yet load, by running the internal PIE against the project PHP (`--with-php-path`, `--skip-enable-extension`). The generated `php.ini` loads those modules, so fresh checkouts reproduce PIE-managed extensions.

As discouraged legacy compatibility, the backend also provisions `pecl-extensions` after PIE (`source/polka/backend/pecl.d`). It resolves official PECL metadata, installs matching Windows DLLs or builds Linux sources with target-matched PHP development tools, tracks installed artifacts beside the PHP runtime, and performs a final `php.ini` synchronization. PIE remains the preferred provider.

Finally, on Windows environments with `settings.php.extension-sdk` enabled, `source/polka/backend/msvc.d` locates a host MSVC toolchain through `vswhere.exe`, captures the variables `vcvarsall.bat` adds, and stores them in `.polka/envs/msvc/<environment>.json`. A missing toolchain is reported through `InstallOptions.warn` rather than failing the install. `polka.cli` applies the capture when composing `sh`, `exec`, and worker environments (`Store.msvcToolchain`), before project env files so user values win.

## Internal Tools

Internal tools are consumed by Polka commands rather than exposed to projects. `source/polka/backend/internal_tools.d` installs them through the same cache pipeline into a machine-global tools directory (`POLKA_TOOLS_DIR`, else `<POLKA_CACHE_DIR>/internal-tools`, else `<user cache dir>/polka/tools`), which acts as the `envsDir` of the reserved `_internal` environment:

- `<tools dir>/polka._internal.yaml` records internal tool versions (seeded from the shared default tool versions, user-overridable).
- `<tools dir>/installed.json` reuses the standard install state; `Store.ensureInternalTool` skips work when the recorded install still resolves on disk, and serializes concurrent cross-project installs with a per-tool lock file.
- Internal PHP installs run the php post-install hook with a synthetic environment enabling a curated broad set of extensions (`polka.tools.internalPHPExtensions`) — filtered to those whose module file exists in the install's `ext/` directory, always including the TLS baseline (`curl`, `openssl`, `mbstring`, `zip`) — so composer create-project scaffolders and PIE cover most use cases and can reach registries over HTTPS.

Manifests may declare `internal-only: true`: such tools report no version for project environments, expose no dispatch or shim commands, and reject `tools.<id>` project config with a "managed internally" validation error. `polka create-project` uses internal PHP + Composer to download a scaffold with `--no-install --no-scripts` before any project config exists. It then initializes the detected framework and installs project tools. Framework configuration hooks run before building Composer's environment so managed credentials are present in framework files and runtime variables; the managed database starts before Composer scripts unless `--no-scripts` was requested. Composer's `create-project` resumes without a package argument in the scaffold directory. This preserves Composer's root-package, dependency and project hooks in their normal order, using project PHP and the managed shell environment. Framework configuration hooks run again afterward in case Composer rewrote files. Packages without configured PHP or Composer fall back to the internal hosts. `polka ext` and the install pipeline use internal PHP + PIE (`source/polka/backend/pie.d`) to manage PHP extensions for project runtimes. Dependency-only examples include Erlang for RabbitMQ and the PHP devel/SDK payloads used by native extension builds.

## Dispatch Flow

Managed command shims in `.polka/bin` call back into Polka:

```text
.polka/bin/php
`-- polka --root <root> dispatch php ...
    `-- Store.resolveTool("php")
        `-- Registry.resolveDispatchRequestForEnvironment("php", environment)
```

Dispatch resolution uses the active environment recorded in `.polka/run/current`, or `default` from `polka.yaml` when no local override is selected. It maps command names to the configured provider, reads that environment's definition from `polka.yaml` or `polka.<name>.yaml`, and locates the installed executable under `.polka/envs`. Tool-specific command mappings are declared by manifests and exposed through `polka.tools.Registry`.

## Runtime Services

Runtime services are split between `polka.service` and `polka.cli`:

- PHP built-in server, nginx, Apache, and FrankenPHP serving remain command/runtime concerns in `polka.cli`.
- Managed database lifecycle is in `polka.service`.
- Mailpit startup, shutdown, and status are in `polka.service`, with CLI adapters for command output and test hooks.
- Meilisearch startup, shutdown, and status are in `polka.service`, with CLI adapters for command output and test hooks.
- Redis startup, shutdown, and status are in `polka.service`, with CLI adapters for command output and test hooks.
- Traefik startup, shutdown, and status are in `polka.service`, with CLI adapters for command output and test hooks.
- Background worker startup, shutdown, and status are in `polka.service`; the CLI injects the managed shell environment and managed-tool target resolution so worker commands such as `php artisan queue:work` run against the real installed binaries.
- phpMyAdmin startup, shutdown, status, and managed MySQL/MariaDB storage import are in `polka.service`; CLI supplies the PHP/nginx/Apache/FrankenPHP web runtime callbacks.
- Certificates remain CLI-managed assets and are passed to services through callback adapters.
- The shell command composes environment variables and `PATH` behavior around the active environment.

The database tool plugins install and dispatch database clients, while database server lifecycle logic lives in `polka.service`. PostgreSQL uses `initdb`, `postgres`, `createdb`, `pg_ctl`, `psql`, and `pg_dump`, with client authentication supplied through a project-local `.pgpass` file.

## Dependency Rules

Keep package dependencies moving in this direction:

```text
polka.cli -> polka.backend -> polka.plugins -> polka.tools -> polka.config -> polka.support
polka.cli -> polka.backend -> polka.tools -> polka.config -> polka.support
polka.cli -> polka.backend -> polka.config -> polka.support
polka.cli -> polka.service -> polka.tools -> polka.config -> polka.support
polka.backend -> polka.config -> polka.support
polka.backend -> polka.service -> polka.tools -> polka.config -> polka.support
```

Avoid these dependencies:

- `polka.tools -> polka.backend`
- `polka.plugins -> polka.backend`
- `polka.config -> polka.backend`
- `polka.config -> polka.tools`
- `polka.service -> polka.backend`
- `polka.support -> polka.{config,tools,plugins,service,backend,cli}`

Plugin hooks receive `ToolInstallContext`, which contains layout paths and install results rather than a `Store`. This keeps `polka.tools` independent from backend orchestration.

## Adding A Managed Tool

To add a new managed tool:

1. Add a tool ID in `source/polka/tools/ids.d` (optional convenience constant for built-in D callers) and create `tools/manifests/<id>.yaml`.
2. Add a plugin constructor in `source/polka/tools/plugins.d` (or a focused tool module if the tool needs custom D hooks).
3. Declare any versionless settings (`settings: <key>: bool|port|string`), shared settings namespace (`settings-from`), mutually exclusive tool peers (`exclusive-with`), version bounds (`version-constraint`), install-time dependencies (`dependencies`), or generated command wrappers (`wrappers`) in `tools/manifests/<id>.yaml`; version lookup from `Environment.toolVersion(id)`, version-constraint checks, port range checks, and `<id> configuration requires version` checks are handled automatically by the manifest plugin and `polka.tools.Registry`.
4. Implement a custom `validate` hook only if the tool has cross-field invariants beyond scalar setting types and version bounds (for example distinct port pairs or non-whitespace credentials).
5. Declare `install.candidates` in the manifest. Add `dispatch.candidates` only to override commands whose executable differs from the install candidates (for example a multi-binary tool); every dispatch command without an override resolves to the install candidates. Unix executable permission bits (`0755`) are applied automatically during post-install.
6. Declare `download` (`github` or `release-index` plus `assets`) in the manifest so the default manifest downloader handles release resolution, checksum verification, and caching automatically; add a custom D `download` hook only when upstream discovery requires custom parsing beyond `github` and `release-index`.
7. Add a focused per-tool D module under `source/polka/tools/` only when the tool requires custom validation, download, dependency, or post-install hooks.
8. Register the plugin in `defaultPlugins` (`source/polka/tools/plugins.d`).
9. Add `unittest` blocks in `polka.tools` for registry, manifest, candidate, and downloader behavior.
10. Add `polka.backend` or `polka.cli` tests only when the new tool changes project state, shims, command output, or runtime behavior.

Do not add runtime plugin loading without a separate design. That would need config schema, trust/security rules, loading behavior, versioning, and error reporting.

## Testing Expectations

Use the package boundary to choose tests:

- `polka.support`: YAML parsing/comment preservation, archive extraction/traversal guards, crypto/TLS/AES-GCM, HTTP retry logic, and XML parsing.
- `polka.tools`: plugin registry, candidate paths, download asset selection, archive extraction, and post-install hooks.
- `polka.config`: normalization behavior and manifest version validation.
- `polka.backend`: store behavior, config persistence, install materialization, shim syncing, tool resolution, runtime state, and service orchestration.
- `polka.cli`: command parsing, user-facing output, command selection, and full command workflows (`source/polka/cli/cli_e2e_test.d`).

The default acceptance check is:

```bash
dub test
```
