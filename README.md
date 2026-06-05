# Polka

Polka is a CLI tool for PHP virtual environment management. It provides a consistent interface for installing and managing multiple local versions of PHP, Composer, Node.js, and other development tools on a per-project basis. Polka also includes features for serving web applications with built-in support for nginx, phpMyAdmin, and Mailpit.

## Quick start

```bash
go test ./...
go run . init
go run . new blog
go run . install blog
go run . cert-install
go run . serve public
go run . stop
go run . exec php -v
go run . sh
go run . list
go run . use blog
go run . status
./.polka/bin/php -v
```

`polka status` shows the active environment, prints each configured tool on its own line, includes the resolved web server URL, and reports whether the webserver, phpMyAdmin, managed database, and Mailpit are running.

`polka init` creates a local `.polka/` directory, writes dispatcher shims in `.polka/bin` that forward to the current `polka` executable, syncs the active environment's dispatch shims in `.polka/bin`, and writes `polka.yaml` if it does not exist.

Use `polka new <name> [--php VERSION] [--composer VERSION] [--nodejs VERSION]` to create a new named environment definition in `polka.<name>.yaml`. When the flags are omitted, Polka currently defaults to `php=8.4`, `composer=2.8`, and `nodejs=24`.

Use `polka install [name]` to install every configured tool version for an environment. When `name` is omitted, Polka installs the current environment and prints which one it selected. If no current environment is selected, Polka uses `default` from `polka.yaml`. Polka first checks the global cache, then downloads any missing versions into that cache, and finally copies the cached payloads into the project-local `.polka/envs` layout. The `mago` tool key installs the Mago binary and creates a `mago` command shim. The `sqlite` tool key installs SQLite's command-line tools and creates a `sqlite3` command shim. The `phpmyadmin` tool key installs the phpMyAdmin web app archive under `.polka/envs/phpmyadmin/<version>`, writes a generated `config.inc.php` with a fresh `blowfish_secret`, and carries its UI `port` and `https` settings; it does not create a command shim.

The shims in `.polka/bin` mirror the active environment's configured tools. A configured `nodejs` version produces `node`, `npm`, and `npx` shims, while the `nodejs` name itself remains config-only. If the current environment does not define a managed tool, Polka removes that local shim instead of leaving a dispatcher that would fail at runtime.

Use `polka sh` to open an interactive shell that resolves commands in this order: `.polka/bin`, then `vendor/bin`, then the inherited system `PATH`. That means a local Polka-managed `composer` shim wins over a globally installed `composer`, while still falling back to project-local Composer plugins in `vendor/bin` and finally to whatever the system shell already exposes. On Windows, Polka also generates temporary `.cmd` wrappers for extensionless Composer PHP proxies and for shell launchers that have a matching `.php` source in `vendor/bin`, so commands such as `drush` run through the local PHP CLI instead of relying on `sh`.

Use `polka exec <command> [args...]` when you want that same resolution order and runtime environment for a single command without opening an interactive shell. For example, `polka exec drush status` and `polka exec php -v` resolve tools through `.polka/bin`, the nearest `vendor/bin`, and then the inherited system `PATH` exactly the same way as `polka sh`.

Polka also composes custom runtime environment variables for the active environment from four sources in this precedence order (lowest to highest): inherited process env, project `.env`, the current environment file's `env-file`, and the current environment file's `env-vars`. The `.env` file is loaded automatically from the directory containing `polka.yaml` when present, `env-file` paths are resolved relative to that same directory (unless absolute), and `env-vars` always win when keys overlap.

Use the stable helper scripts in `.polka/` when you want to activate that same command resolution in the current shell instead of opening a child shell. Polka installs these wrappers when it initializes the local state directory:

- PowerShell: `.\.polka\session-start.ps1`
- POSIX: `. ./.polka/session-start`

Those wrappers call `polka session start`, source the generated activation script for you, and prepend `.polka/bin`, the nearest `vendor/bin`, and the inherited system `PATH` in the same order as `polka sh`. On Windows, the session flow reuses the same temporary vendor/bin `.cmd` wrappers as `polka sh` for extensionless Composer PHP proxies and shell launchers with a matching `.php` source.

To deactivate the current shell session and restore the exact pre-session values for every variable Polka changed (including `PATH`), use the matching wrapper:

- PowerShell: `.\.polka\session-stop.ps1`
- POSIX: `. ./.polka/session-stop`

The lower-level `polka session start` and `polka session stop` commands remain available for now; they print the transient activation or deactivation script path that the stable wrappers source for you.

Use `polka serve [docroot] [--server HOST:PORT] [--watch]` to start the active environment's web server. When `docroot` is omitted, Polka uses `docroot` from the current environment file. By default, Polka starts the webserver in the background, waits for it to begin listening, and records runtime state so `polka stop` can stop it later. Pass `--watch` to keep the previous foreground behavior in the current terminal. When the current environment defines `nginx`, Polka starts `php-cgi` on an internal loopback port and runs nginx with a generated FastCGI config; otherwise it falls back to PHP's built-in web server with a generated router that serves existing static files with explicit MIME types and forwards missing requests into the app router or front controller. When the current environment defines `mailpit`, Polka starts Mailpit before the webserver and records runtime state so `polka stop` can stop it later. Mailpit listens on `127.0.0.1:1025` for SMTP and `127.0.0.1:8025` for the web UI by default; set `mailpit.smtp-port` or `mailpit.ui-port` to override those ports. Set `mailpit.https: true` to serve the Mailpit UI over HTTPS and enable SMTP STARTTLS using Polka's generated local certificate. When the current environment defines `phpmyadmin`, Polka starts the installed phpMyAdmin web app before the webserver; if a managed database is configured, Polka also imports phpMyAdmin's bundled `sql/create_tables.sql` into the managed database before starting the UI. Set `phpmyadmin.port` to choose the UI port and `phpmyadmin.https: true` to serve it through nginx with Polka's local certificate. `polka status` prints the full Mailpit and phpMyAdmin UI URLs. Polka reads `server.hostname`, `server.port`, and `server.https` from the current environment file, and falls back to `http://localhost:8000` when that config is absent. HTTPS requires nginx at start time. Polka uses one generated server certificate from the global Polka cache for all HTTPS environments; it covers `localhost`, `*.localhost`, `127.0.0.1`, and `::1`, and is signed by a generated local Polka CA. With the default cache layout, the certificate material lives under the cache's `polka/cert` directory. Run `polka cert-install` at any time to clear and regenerate that global CA/server certificate pair, then install the CA certificate into the current user's trust store on Windows or macOS. Hostnames ending in `.localhost`, such as `blog.localhost`, are bound to `127.0.0.1` so the site is local-only and works without editing the hosts file. Automatic nginx downloads are currently implemented on Windows amd64. Automatic Mailpit downloads are currently implemented for Windows amd64 and Linux amd64. Automatic phpMyAdmin downloads use the official cross-platform zip archive. The same runtime env composition used by `polka sh` also applies to `polka serve`, generated `.polka/bin` dispatch shims, and database client/import/export commands.

Use `polka stop` to stop the active environment's background webserver, phpMyAdmin, managed database, and Mailpit when configured.

When an environment defines `php-extensions`, `polka install` also writes a generated `php.ini` next to the installed PHP executable so those extensions are explicitly enabled or disabled for that environment. If `composer` is configured for that environment, `openssl` and `zip` are enabled by default unless `php-extensions` explicitly sets either one to `false`.

Use `polka config [name] --php <version> --composer <version> --nodejs <version>` to update an existing environment definition. When `name` is omitted, Polka updates the current environment, or `default` from `polka.yaml` when no local override is selected. Config files store version labels, not machine-specific executable paths or the active environment selection:

Managed database versions are stored as `tools.mysql` and/or `tools.mariadb`; root-level `database.engine` selects the primary database for runtime commands, and shared options such as `database.port` live alongside it.

Note that current schema is not yet stable, so expect breaking changes to config file structure and field names until version 1.0.

```yaml
# polka.yaml
version: 0.1
root: .polka
tools:
  php: 8.4
  composer: 2.8
  nodejs: 24
  mago: "1.27"
  nginx: 1.30
  sqlite: "3.53"
  mariadb: "11.8"
  phpmyadmin:
    version: 5.2
    port: 8082
    https: true
  mailpit:
    version: "1.30"
    smtp-port: 1025
    ui-port: 8025
    https: true
docroot: public
database:
  engine: mariadb
  port: 3306
env-file: .env.local
env-vars:
  APP_ENV: development
  APP_DEBUG: "1"
server:
  hostname: blog.localhost
  port: 8443
  https: true
php-extensions:
  openssl: true
  xdebug: false
```

```yaml
# polka.legacy.yaml
tools:
  php: 8.2
  composer: 2.6
```

Polka resolves those versions against the local install layout under `.polka/envs`:

```text
.polka/
|-- envs/
|   |-- composer/
|   |   `-- 2.8/
|   |       `-- bin/composer[.cmd|.bat|.exe|.phar]
|   |-- nodejs/
|   |   `-- 24/
|   |       `-- node[.exe]
|   |-- phpmyadmin/
|   |   `-- 5.2/
|   |       `-- index.php
|   |-- php/
|   |   `-- 8.4/
|   |       `-- bin/php[.exe|.cmd|.bat]
|   `-- sqlite/
|       `-- 3.53/
|           `-- sqlite3[.exe]
`-- bin/
  |-- node[.cmd]
  |-- npm[.cmd]
  |-- npx[.cmd]
  `-- sqlite3[.cmd]
```

For example, this sequence records version labels in `polka.blog.yaml`, installs that environment from cache or download, and then dispatches through the generated shim:

```bash
go run . new blog
go run . install blog
go run . use blog
./.polka/bin/php -v
```

If you want non-default versions at creation time, pass them explicitly:

```bash
go run . new legacy --php 8.2 --composer 2.6 --nodejs 22
```

That keeps Polka config files portable across machines while letting each machine reuse a shared cache and install project-local copies from it.

During development you can override the state directory while keeping the config file next to it:

```bash
go run . --root ./.polka-dev init
```

## Next implementation steps

1. Add cache metadata and eviction so old downloaded versions can be pruned safely.
2. Add shell hooks or direnv-style integrations around `polka session start` / `polka session stop`.
3. Add config schema validation.
