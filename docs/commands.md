# Polka Commands

This page summarizes Polka's command surface. Run `polka help` or `polka <command> --help` for the exact usage text built into the CLI.

## Global Flags

```bash
polka [--root PATH] <command> [options]
```

Use `--root PATH` to override the local state directory. The default is `.polka`.

## Environment Commands

### `polka init`

Creates the local `.polka` directory, bootstraps `polka.yaml` when it does not exist, writes stable shell-session helper scripts, and syncs the active environment's dispatch shims into `.polka/bin`.

### `polka new <name>`

Creates a named environment definition in `polka.<name>.yaml`.

```bash
polka new blog
polka new legacy --php 8.2 --composer 2.6 --nodejs 22
polka new app --db-engine mariadb --db-version 11.8 --db-port 3306
```

When tool flags are omitted, Polka currently defaults to `php=8.4`, `composer=2.8`, and `nodejs=24`. Database settings require `--db-engine` and `--db-version` together. `--db-port` is optional.

### `polka config [name]`

Creates or updates an environment definition. The default environment is stored in `polka.yaml`; named environments are stored in `polka.<name>.yaml`.

```bash
polka config --php 8.4 --composer 2.8 --nodejs 24
polka config blog --db-engine mysql --db-version 8.0 --db-port 3306
```

When `name` is omitted, Polka updates the current environment, falling back to `default` when no local override is selected.

### `polka install [name]`

Installs every configured tool version for an environment.

```bash
polka install
polka install blog
```

When `name` is omitted, Polka installs the current environment and prints which one it selected. If no current environment is selected, Polka uses `default` from `polka.yaml`. Polka first checks the global cache, downloads missing versions into that cache, and copies cached payloads into the project-local `.polka/envs` layout.

The `.polka/bin` shims mirror the active environment's configured tools. A configured `nodejs` version produces `node`, `npm`, and `npx` shims, while `nodejs` itself remains config-only. If the current environment does not define a managed tool, Polka removes that local shim instead of leaving a dispatcher that would fail at runtime.

### `polka list`

Lists environments discovered from `polka.yaml` and `polka.<name>.yaml` files. The active environment is marked with `*`.

### `polka use <name>`

Selects one environment as the local override. Use `default` to clear the override and fall back to `polka.yaml`.

### `polka status`

Alias: `polka info`

Shows the active environment, prints each configured tool on its own line, includes the resolved web server URL, and reports whether the webserver, phpMyAdmin, managed database, and Mailpit are running.

### `polka remove <name>`

Removes a named environment definition. The default environment in `polka.yaml` cannot be removed.

## Runtime Commands

### `polka sh`

Alias: `polka shell`

Opens an interactive shell that resolves commands in this order:

1. `.polka/bin`
2. `vendor/bin`
3. inherited system `PATH`

This means a local Polka-managed `composer` shim wins over a globally installed `composer`, while still falling back to project-local Composer plugins in `vendor/bin` and finally to system commands.

On Windows, Polka launches PowerShell and generates temporary `.cmd` wrappers for extensionless Composer PHP proxies and shell launchers with a matching `.php` source in `vendor/bin`. Commands such as `drush` then run through the local PHP CLI instead of relying on `sh`.

### `polka exec <command> [args...]`

Runs one command with the same resolution order and runtime environment as `polka sh`.

```bash
polka exec php -v
polka exec drush status
```

### `polka session start`

Generates an activation script for the current shell. Prefer the stable project-local wrappers created by `polka init`:

```powershell
. .\.polka\session-start.ps1
```

```bash
. ./.polka/session-start
```

The session uses the same command resolution order as `polka sh`: `.polka/bin`, then `vendor/bin`, then the inherited system `PATH`. On Windows, the session flow reuses the same temporary vendor `.cmd` wrappers as `polka sh`.

### `polka session stop`

Generates a deactivation script that restores the exact pre-session values for every variable Polka changed, including `PATH`. Prefer the stable project-local wrappers:

```powershell
. .\.polka\session-stop.ps1
```

```bash
. ./.polka/session-stop
```

## Web And Service Commands

### `polka serve [docroot]`

Alias: `polka start [docroot]`

Starts the active environment's web server.

```bash
polka serve
polka serve public
polka serve public --server blog.localhost:8443
polka serve --watch
```

When `docroot` is omitted, Polka uses `docroot` from the current environment file. When `--server` is omitted, Polka reads `server.hostname`, `server.port`, and the root-level `https` setting from the current environment file, defaulting to `http://localhost:8000`.

By default, Polka starts the webserver in the background, waits for it to begin listening, and records runtime state so `polka stop` can stop it later. Pass `--watch` to keep the webserver attached to the current terminal.

When the current environment defines `nginx`, Polka starts `php-cgi` on an internal loopback port and runs nginx with a generated FastCGI config. Otherwise, it falls back to PHP's built-in web server with a generated router that serves existing static files with explicit MIME types and forwards missing requests into the app router or front controller.

If the environment defines `mailpit`, `phpmyadmin`, or a managed database, Polka starts those local services before the webserver. `polka status` prints the full Mailpit and phpMyAdmin UI URLs.

HTTPS requires nginx at start time. Polka uses one generated server certificate from the global Polka cache for HTTPS environments. The generated certificate covers `localhost`, `*.localhost`, `127.0.0.1`, and `::1`, and is signed by a generated local Polka CA. Hostnames ending in `.localhost`, such as `blog.localhost`, are bound to `127.0.0.1` without editing the hosts file.

### `polka stop`

Stops the active environment's background webserver, phpMyAdmin, managed database, and Mailpit when they are running.

### `polka cert-install`

Clears and regenerates the global Polka CA/server certificate pair, then installs the CA certificate into the current user's trust store on Windows or macOS. On other platforms, Polka prints an error with the certificate path so it can be installed manually.

## Database Commands

Polka dispatches database commands to the primary database selected by `database.engine` in the current environment. Managed database versions are stored as `tools.mysql` or `tools.mariadb`; shared options such as `database.port` live under the root-level `database` section.

### `polka db [args...]`

Forwards arguments to the active environment's database client. Polka injects connection options for the managed local server unless native connection options such as `--defaults-file`, `--host`, `--port`, `--socket`, or a non-TCP `--protocol` are provided.

```bash
polka db --version
polka db --execute "show databases"
polka db --db-name app_test --execute "show tables"
```

Use `polka db client [args...]` to force client dispatch when the first argument is a reserved Polka database subcommand such as `status`.

### `polka db start`

Initializes the local database data directory if needed and starts the managed database server.

### `polka db stop`

Stops the database server previously started by Polka.

### `polka db status`

Shows whether the managed database server is running and which address it uses.

### `polka db export <path.sql|path.sql.gz>`

Dumps the selected database to a SQL file. Use `--db-name NAME` to target a database other than the default database named after the active environment.

### `polka db import <path.sql|path.sql.gz>`

Loads SQL from a `.sql` or `.sql.gz` file into the active database server. Use `--db-name NAME` to target a database other than the default database named after the active environment.

## Managed Tool Notes

The `mago` tool key installs the Mago binary and creates a `mago` command shim.

The `sqlite` tool key installs SQLite's command-line tools and creates a `sqlite3` command shim.

The `phpmyadmin` tool key installs the phpMyAdmin web app archive under `.polka/envs/phpmyadmin/<version>`, writes a generated `config.inc.php` with a fresh `blowfish_secret`, uses managed database credentials to skip the phpMyAdmin login screen when a managed database is configured, and carries its UI `port` setting. It inherits HTTPS from the environment's root-level `https` setting. It does not create a command shim.

When an environment defines `php-extensions`, `polka install` writes a generated `php.ini` next to the installed PHP executable so those extensions are explicitly enabled or disabled for that environment. If `composer` is configured for that environment, `openssl` and `zip` are enabled by default unless `php-extensions` explicitly sets either one to `false`.

Polka composes runtime environment variables for the active environment from four sources in this precedence order, lowest to highest:

1. inherited process environment
2. project `.env`
3. current environment file's `env-file`
4. current environment file's `env-vars`

The `.env` file is loaded automatically from the directory containing `polka.yaml` when present. `env-file` paths are resolved relative to that same directory unless absolute, and `env-vars` always win when keys overlap. This runtime environment applies to `polka sh`, `polka exec`, `polka serve`, generated `.polka/bin` dispatch shims, and database client/import/export commands.

## Platform Notes

Automatic nginx downloads are currently implemented on Windows amd64.

Automatic Mailpit downloads are currently implemented for Windows amd64 and Linux amd64.

Automatic phpMyAdmin downloads use the official cross-platform zip archive.
