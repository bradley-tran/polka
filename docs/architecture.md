# Polka Architecture

Polka is a Go CLI for managing project-local PHP development environments. The project is organized around a small CLI layer, a backend orchestration layer, shared config types, and a tool plugin package that knows how to install and dispatch managed tools.

## Package Map

```text
.
|-- main.go
|-- cli/
|-- backend/
|-- config/
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
- `DatabaseConfig`
- `MailpitConfig`
- `PHPMyAdminConfig`
- `ServerConfig`

This package exists to avoid import cycles. Both `backend` and `tools` can depend on config types without either package importing the other.

### `tools`

The `tools` package owns managed tool behavior:

- tool IDs such as `PHP`, `Composer`, `NodeJS`, `Mago`, `Nginx`, `Mailpit`, `PHPMyAdmin`, `MySQL`, and `MariaDB`
- plugin interfaces and registry
- embedded YAML manifests for built-in plugin metadata
- install candidate paths
- dispatch command mappings
- download catalogs and archive extraction
- per-tool Go hooks for dynamic downloads or post-install behavior
- PHP extension post-install config generation

The current plugin system is internal and compile-time only. Built-in plugin metadata lives in `tools/manifests/*.yaml` and is embedded into the binary; Polka does not load third-party plugins from disk or from `polka.yaml`. The manifest parser accepts bytes so future external loading can reuse the schema, but that loading behavior is intentionally not implemented yet.

## Tool Install Flow

`polka install <environment>` follows this flow:

1. `cli` resolves the requested environment name and calls `backend.Store.InstallWithProgress`.
2. `backend.Store` loads and normalizes `polka.yaml` for the default environment or `polka.<name>.yaml` for named environments.
3. `backend.Store` asks `tools.Registry` to validate the environment and produce ordered install requests.
4. For each requested tool, `backend.Store` checks the global cache.
5. If the cache is missing, `tools.HTTPDownloader` invokes the matching plugin download hook.
6. `backend.Store` copies the cached tool into `.polka/envs/<tool>/<version>`.
7. The tool plugin may run a post-install hook using `tools.InstallContext`.
8. `backend.Store` returns install results to the CLI.

The global cache and project-local install layouts are intentionally separate. The cache stores reusable downloaded payloads; `.polka/envs` stores project-local copies selected by the active environment config.

## Dispatch Flow

Managed command shims in `.polka/bin` call back into Polka:

```text
.polka/bin/php
`-- polka --root <root> dispatch php ...
    `-- backend.Store.ResolveTool("php")
        `-- tools.Registry.ResolveDispatchRequest("php")
```

Dispatch resolution uses the active environment recorded in `.polka/run/current`, or `default` from `polka.yaml` when no local override is selected. It maps command names to their config tool, reads that environment's definition from `polka.yaml` or `polka.<name>.yaml`, and locates the installed executable under `.polka/envs`.

Node.js is config-only as `nodejs`, but it exposes `node`, `npm`, and `npx` dispatch commands. The `nodejs` command itself is not generated as an active shim. Mago is configured with `mago` and exposes the `mago` dispatch command. phpMyAdmin does not generate a command shim either; Polka installs its web app archive, writes its generated `config.inc.php`, and uses its UI port/HTTPS settings when the CLI starts the managed phpMyAdmin service.

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
cli -> backend -> tools -> config
cli -> backend -> config
backend -> config
```

Avoid these dependencies:

- `tools -> backend`
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
6. Add embedded manifest download catalog entries if Polka should download it automatically.
7. Add a focused per-tool Go file only for hook wiring, dynamic download logic, validation, or post-install behavior.
8. Register the plugin in `tools.DefaultPlugins`.
9. Add tests in `tools` for registry, manifest, candidate, and downloader behavior.
10. Add backend or CLI tests only when the new tool changes project state, shims, command output, or runtime behavior.

Do not add runtime plugin loading without a separate design. That would need config schema, trust/security rules, loading behavior, versioning, and error reporting.

## Testing Expectations

Use the package boundary to choose tests:

- `tools`: plugin registry, candidate paths, download asset selection, archive extraction, and post-install hooks.
- `config`: normalization behavior when shared helpers become complex enough to warrant direct tests.
- `backend`: store behavior, config persistence, install/copy behavior, shim syncing, tool resolution, runtime state, and service orchestration.
- `cli`: command parsing, user-facing output, command selection, and full command workflows.

The default acceptance check is:

```bash
go test ./...
```
