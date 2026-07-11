# Polka Architecture

Polka is a Go CLI for managing project-local PHP development environments. The project is organized around a small CLI layer, a backend state layer, shared config types, service runtime orchestration, a higher-level plugin registry, and a tool package that knows how to install and dispatch managed tools.

## Package Map

```text
.
|-- main.go
|-- cli/
|-- backend/
|-- config/
|-- plugins/
|-- service/
`-- tools/
```

### `cli`

The `cli` package owns command parsing, user-facing command output, and command-specific workflows. It uses Cobra to expose commands such as `init`, `new`, `install`, `serve`, `stop`, `exec`, `sh`, `db`, and `status`.

The CLI should stay thin. It resolves flags and arguments, asks `backend.Store` for stateful operations, and handles process execution or runtime command flows where those are command-specific.

### `backend`

The `backend` package owns Polka project state and orchestration:

- locating the project and `polka.yaml`
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
- `ToolsConfig`
- `SettingsConfig`
- `DatabaseConfig`
- `MailpitConfig`
- `PHPMyAdminConfig`
- `ServerConfig`
- `WorkerConfig` under the top-level `workers` map
- framework IDs stored as `framework` on project and environment files

This package exists to avoid import cycles. Both `backend` and `tools` can depend on config types without either package importing the other.

`ToolsConfig` stores only managed tool version labels. Versionless tool options, such as Mailpit ports and the phpMyAdmin UI port, live in sibling `SettingsConfig` data and are merged into the internal `Environment` model during config loading.

### `plugins`

The `plugins` package owns Polka's higher-level built-in plugin registry. It groups installable tool plugins from `tools` with framework plugins such as `cakephp`, `codeigniter`, `drupal`, `wordpress`, `laravel`, and `symfony`.

Framework plugins provide config defaults and optional hooks for framework-common PHP extensions, runtime environment variables, OPcache directives, post-Composer secret file generation, and nginx config generation. Database driver extensions belong to the configured database tool rather than the framework. Built-in framework metadata lives in `plugins/manifests/*.yaml` and is embedded into the binary; the manifest data selects reusable Go strategies for framework-specific runtime environment and post-Composer behavior. In v1, framework init is config-only and framework nginx hooks delegate to the generic front-controller config.

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
- PHP extension dependencies declared by tool manifests
- install candidate paths
- dispatch command mappings (a command without an explicit dispatch candidate falls back to the tool's install candidates)
- download templates, release resolution, checksums, cache metadata, and archive extraction
- per-tool Go hooks for dynamic downloads or post-install behavior
- PHP extension post-install config generation

The current plugin system is internal and compile-time only. Built-in tool metadata lives in `tools/manifests/*.yaml`, built-in framework metadata lives in `plugins/manifests/*.yaml`, and both are embedded into the binary; Polka does not load third-party plugins from disk or from `polka.yaml`. The manifest parsers accept bytes so future external loading can reuse the schemas, but that loading behavior is intentionally not implemented yet.

## Tool Install Flow

`polka install [tool:version] [--env <environment>] [--force]` follows this flow:

1. `cli` resolves the requested environment name from `--env`, the active environment, or `default`.
2. For an explicit `tool:version`, `cli` calls `backend.Store.InstallToolWithProgress`; otherwise it calls `backend.Store.InstallWithProgress`.
3. `backend.Store` loads and normalizes `polka.yaml` for the default environment or `polka.<name>.yaml` for named environments, then validates the install request(s). It builds the effective PHP extension set from framework-common requirements, every configured tool's manifest requirements, and explicit environment overrides in that order.
4. For each requested tool, `backend.Store` first consults the project-local install state in `.polka/envs/installed.json`. Without `--force`, a tool recorded there whose installed executable still resolves on disk skips download and extraction and is reported as unchanged; its post-install hook still runs so configuration changes (PHP extensions, memory limit, OPcache) are applied. Explicit `tool:version` installs always reinstall.
5. Otherwise, `backend.Store` checks the global cache metadata and cached payload checksum.
6. If the cache is missing or invalid, `tools.HTTPDownloader` invokes the matching plugin download hook.
7. `backend.Store` installs from the cached payload into `.polka/envs/<tool>/<version>`; archive payloads are extracted on demand, while single-file payloads such as PHARs are placed at their expected install path.
8. The tool plugin may run a post-install hook using `tools.InstallContext`.
9. `backend.Store` records each fully installed tool version in `.polka/envs/installed.json` (including tools that succeeded before another tool's failure aborted the run) and returns install results to the CLI.

For an explicit `tool:version`, the new version is written to the environment config only after the install succeeds, so a failed resolve, download, or install leaves the previous configuration untouched.

The global cache and project-local install layouts are intentionally separate. The cache stores reusable downloaded payloads under per-tool metadata; `.polka/envs` stores project-local installs selected by the active environment config, alongside the `installed.json` install state (deleting `.polka/envs` resets both together). For user-facing managed tool behavior, command shims, PHP runtime generation, and platform support, see [tools.md](tools.md).

After the tool installs, `backend.Store` provisions any PIE-managed `vendor/name` entries under `php-extensions` that the environment's installed PHP does not yet load, by running the internal PIE against the project PHP (`--with-php-path`, `--skip-enable-extension`). The generated `php.ini` loads those modules, so fresh checkouts reproduce PIE-managed extensions.

As discouraged legacy compatibility, the backend also provisions `pecl-extensions` after PIE. It resolves official PECL metadata, installs matching Windows DLLs or builds Linux sources with target-matched PHP development tools, tracks installed artifacts beside the PHP runtime, and performs a final `php.ini` synchronization. PIE remains the preferred provider.

## Internal Tools

Internal tools are consumed by Polka commands rather than exposed to projects. `backend/internal_tools.go` installs them through the same cache pipeline into a machine-global tools directory (`POLKA_TOOLS_DIR`, else `<POLKA_CACHE_DIR>/internal-tools`, else `<user cache dir>/polka/tools`), which acts as the `EnvsDir` of the reserved `_internal` environment:

- `<tools dir>/polka._internal.yaml` records internal tool versions (seeded from the shared default tool versions, user-overridable).
- `<tools dir>/installed.json` reuses the standard install state; `Store.EnsureInternalTool` skips work when the recorded install still resolves on disk, and serializes concurrent cross-project installs with a per-tool lock file.
- Internal PHP installs run the php post-install hook with a synthetic environment enabling a curated broad set of extensions (`tools.InternalPHPExtensions`) — filtered to those whose module file exists in the install's `ext/` directory, always including the TLS baseline (`curl`, `openssl`, `mbstring`, `zip`) — so composer create-project scaffolders and PIE cover most use cases and can reach registries over HTTPS.

Manifests may declare `internal-only: true` (PIE in v1): such tools report no version for project environments, expose no dispatch or shim commands, and reject `tools.<id>` project config with a "managed internally" validation error. `polka create-project` uses internal PHP + composer to scaffold apps before any project config exists; `polka ext` and the install pipeline use internal PHP + PIE (`backend/pie.go`) to manage PHP extensions for project runtimes.

## Dispatch Flow

Managed command shims in `.polka/bin` call back into Polka:

```text
.polka/bin/php
`-- polka --root <root> dispatch php ...
    `-- backend.Store.ResolveTool("php")
     `-- tools.Registry.ResolveDispatchRequestForEnvironment("php", environment)
```

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
```

Avoid these dependencies:

- `tools -> backend`
- `plugins -> backend`
- `config -> backend`
- `config -> tools`
- `service -> backend`

Plugin hooks receive `tools.InstallContext`, which contains layout paths and install results rather than a `backend.Store`. This keeps the tool package independent from backend orchestration.

## Adding A Managed Tool

To add a new managed tool:

1. Add a tool ID in `tools/ids.go`.
2. Add a plugin constructor in `tools/plugins.go` or a new focused tool file.
3. Implement version lookup from `config.Environment`.
4. Implement validation if the tool has nested config.
5. Implement install candidates. Add dispatch candidates only to override commands whose executable differs from the install candidates (for example a multi-binary tool); every dispatch command without an override resolves to the install candidates.
6. Add embedded manifest download assets or a focused download hook if Polka should download it automatically.
7. Add a focused per-tool Go file only for hook wiring, dynamic download logic, validation, or post-install behavior.
8. Register the plugin in `tools.DefaultPlugins`.
9. Add tests in `tools` for registry, manifest, candidate, and downloader behavior.
10. Add backend or CLI tests only when the new tool changes project state, shims, command output, or runtime behavior.

Do not add runtime plugin loading without a separate design. That would need config schema, trust/security rules, loading behavior, versioning, and error reporting.

## Testing Expectations

Use the package boundary to choose tests:

- `tools`: plugin registry, candidate paths, download asset selection, archive extraction, and post-install hooks.
- `config`: normalization behavior when shared helpers become complex enough to warrant direct tests.
- `backend`: store behavior, config persistence, install materialization, shim syncing, tool resolution, runtime state, and service orchestration.
- `cli`: command parsing, user-facing output, command selection, and full command workflows.

The default acceptance check is:

```bash
go test ./...
```
