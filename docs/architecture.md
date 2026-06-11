# Polka Architecture

Polka is a Go CLI for managing project-local PHP development environments. The project is organized around a small CLI layer, a backend orchestration layer, shared config types, a higher-level plugin registry, and a tool package that knows how to install and dispatch managed tools.

## Package Map

```text
.
|-- main.go
|-- cli/
|-- backend/
|-- config/
|-- plugins/
`-- tools/
```

### `cli`

The `cli` package owns command parsing, user-facing command output, and command-specific workflows. It uses Cobra to expose commands such as `init`, `new`, `install`, `serve`, `stop`, `exec`, `sh`, `session`, `db`, and `status`.

The CLI should stay thin. It resolves flags and arguments, asks `backend.Store` for stateful operations, and handles process execution or runtime command flows where those are command-specific.

### `backend`

The `backend` package owns Polka project state and orchestration:

- locating the project and `polka.yaml`
- reading, normalizing, and writing config
- managing `.polka/envs`, `.polka/bin`, `.polka/run`, secrets, and logs
- installing tools from the global cache into the project
- syncing dispatch shims for the active environment
- resolving active tool executable paths
- managing runtime services such as the webserver, database server, Mailpit, certificates, and shell/session state

`backend.Store` is the main entry point for persistent project operations. It delegates tool-specific behavior to `tools.Registry` and `tools.Downloader`.

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
- framework IDs stored as `framework` on project and environment files

This package exists to avoid import cycles. Both `backend` and `tools` can depend on config types without either package importing the other.

`ToolsConfig` stores only managed tool version labels. Versionless tool options, such as Mailpit ports and the phpMyAdmin UI port, live in sibling `SettingsConfig` data and are merged into the internal `Environment` model during config loading.

### `plugins`

The `plugins` package owns Polka's higher-level built-in plugin registry. It groups installable tool plugins from `tools` with framework plugins such as `drupal`, `wordpress`, `laravel`, and `symfony`.

Framework plugins provide config defaults and optional hooks for PHP extensions, runtime environment variables, OPcache directives, post-Composer secret file generation, and nginx config generation. In v1, framework init is config-only and framework nginx hooks delegate to the generic front-controller config.

### `tools`

The `tools` package owns managed tool behavior:

- tool IDs such as `PHP`, `Composer`, `PIE`, `NodeJS`, `Mago`, `Nginx`, `Mailpit`, `PHPMyAdmin`, `MySQL`, `MariaDB`, and `SQLite`
- tool plugin interfaces and registry
- embedded YAML manifests for built-in plugin metadata
- install candidate paths
- dispatch command mappings
- download templates, release resolution, checksums, cache metadata, and archive extraction
- per-tool Go hooks for dynamic downloads or post-install behavior
- PHP extension post-install config generation

The current plugin system is internal and compile-time only. Built-in tool metadata lives in `tools/manifests/*.yaml` and is embedded into the binary; Polka does not load third-party plugins from disk or from `polka.yaml`. The manifest parser accepts bytes so future external loading can reuse the schema, but that loading behavior is intentionally not implemented yet.

## Tool Install Flow

`polka install [tool:version] [--env <environment>]` follows this flow:

1. `cli` resolves the requested environment name from `--env`, the active environment, or `default`.
2. For an explicit `tool:version`, `cli` calls `backend.Store.InstallToolWithProgress`; otherwise it calls `backend.Store.InstallWithProgress`.
3. `backend.Store` loads and normalizes `polka.yaml` for the default environment or `polka.<name>.yaml` for named environments, then validates the install request(s).
4. For each requested tool, `backend.Store` checks the global cache metadata and cached payload checksum.
5. If the cache is missing or invalid, `tools.HTTPDownloader` invokes the matching plugin download hook.
6. `backend.Store` installs from the cached payload into `.polka/envs/<tool>/<version>`; archive payloads are extracted on demand, while single-file payloads such as PHARs are placed at their expected install path.
7. The tool plugin may run a post-install hook using `tools.InstallContext`.
8. `backend.Store` returns install results to the CLI.

The global cache and project-local install layouts are intentionally separate. The cache stores reusable downloaded payloads under per-tool metadata; `.polka/envs` stores project-local installs selected by the active environment config.

## Dispatch Flow

Managed command shims in `.polka/bin` call back into Polka:

```text
.polka/bin/php
`-- polka --root <root> dispatch php ...
    `-- backend.Store.ResolveTool("php")
     `-- tools.Registry.ResolveDispatchRequest("php")
```

Dispatch resolution uses the active environment recorded in `.polka/run/current`, or `default` from `polka.yaml` when no local override is selected. It maps command names to their config tool, reads that environment's definition from `polka.yaml` or `polka.<name>.yaml`, and locates the installed executable under `.polka/envs`. PHAR tools such as Composer and PIE are launched through the environment's managed PHP executable. After a successful dispatched `composer install`, `composer update`, or `composer create-project`, Polka runs the active framework's post-Composer hook so the framework can create or update local secret files from Polka-managed database credentials.

Node.js is config-only as `nodejs`, but it exposes `node`, `npm`, and `npx` dispatch commands. The `nodejs` command itself is not generated as an active shim. PIE and Mago are configured with `pie` and `mago`, and expose matching dispatch commands. phpMyAdmin does not generate a command shim either; Polka installs its web app archive, writes its generated `config.inc.php`, reads managed database credentials from Polka's runtime secrets when a managed database is configured, and uses its UI port plus the environment's root-level HTTPS setting when the CLI starts the managed phpMyAdmin service.

## Runtime Services

Runtime services remain in `backend` and `cli`:

- PHP built-in server and nginx serving are command/runtime concerns.
- Managed database lifecycle is in `backend/database_runtime.go`.
- Mailpit startup, shutdown, and status are command/runtime concerns.
- phpMyAdmin startup, shutdown, status, and managed database storage import are command/runtime concerns.
- Certificates are backend-managed assets.
- Shell and session commands compose environment variables and `PATH` behavior around the active environment.

The database tool plugins install and dispatch database clients, but database server lifecycle logic remains backend orchestration.

## Dependency Rules

Keep package dependencies moving in this direction:

```text
cli -> backend -> plugins -> tools -> config
cli -> backend -> tools -> config
cli -> backend -> config
backend -> config
```

Avoid these dependencies:

- `tools -> backend`
- `plugins -> backend`
- `config -> backend`
- `config -> tools`

Plugin hooks receive `tools.InstallContext`, which contains layout paths and install results rather than a `backend.Store`. This keeps the tool package independent from backend orchestration.

## Adding An Internal Tool

To add a new managed tool:

1. Add a tool ID in `tools/ids.go`.
2. Add a plugin constructor in `tools/plugins.go` or a new focused tool file.
3. Implement version lookup from `config.Environment`.
4. Implement validation if the tool has nested config.
5. Implement install candidates and dispatch candidates.
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
