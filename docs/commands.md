# Polka Commands

This page summarizes Polka's command surface. Run `polka help` or `polka <command> --help` for the exact usage text built into the CLI.

## Global Flags

```bash
polka [--root PATH] <command> [options]
```

Use `--root PATH` to override the local state directory. The default is `.polka`.

## Environment Commands

### `polka init [framework] [--docroot PATH]`

Creates the local `.polka` directory, bootstraps `polka.yaml` when it does not exist, and syncs the active environment's dispatch shims into `.polka/bin`. The bootstrapped default environment enables HTTPS and sets `server.hostname` to `<directory>.localhost`.

```bash
polka init
polka init cakephp
polka init codeigniter
polka init drupal
polka init wordpress
polka init laravel
polka init symfony
polka init --docroot public
polka init drupal --docroot drupal/web
```

Use `--docroot PATH` to set the generated default environment's document root.

When `framework` is `cakephp`, `codeigniter`, `drupal`, `wordpress`, `laravel`, or `symfony`, Polka writes an opinionated default config with a top-level `framework` key, framework docroot, managed tool versions, database settings, and phpMyAdmin settings. Framework init is config-only: it does not create project files, run Composer, install tools, write default PHP extension config, or start services. It fails if `polka.yaml` already exists.

CakePHP uses the `webroot` docroot. CodeIgniter, Laravel, and Symfony use `public`. Drupal uses `web`. These presets include PHP, Composer, Node.js, nginx, MariaDB, phpMyAdmin, and Mailpit. The WordPress preset uses the project root as docroot and includes PHP, nginx, MariaDB, and phpMyAdmin. With a framework, `--docroot PATH` overrides the preset docroot in the generated config.

### `polka new <name>`

Creates a named environment definition in `polka.<name>.yaml`.

```bash
polka new blog
polka new legacy --php 8.2 --composer 2.6 --nodejs 22
polka new app --db-engine mariadb --db-version 11.8 --db-port 3306
polka new reporting --db-engine postgresql --db-version 17 --db-port 5432
```

When tool flags are omitted, Polka currently defaults to `php=8.4`, `composer=2.8`, and `nodejs=24`. Database settings require `--db-engine` and `--db-version` together. `--db-port` is optional.

### `polka create-project <package> [directory] [composer-args...]`

Scaffolds a new application with Polka's internal composer, then initializes Polka in the created directory.

```bash
polka create-project laravel/laravel demo
polka create-project cakephp/app
polka create-project symfony/skeleton api --stability=beta
```

Polka provisions an internal PHP runtime and composer into the global tools directory on first use; they are implementation details and never appear in project config. Remaining arguments pass through to `composer create-project` unchanged. After scaffolding, Polka detects the framework from the package name or scaffolded marker files, writes the matching `polka.yaml` preset (or a plain default config when nothing is detected), and runs the same post-Composer framework hooks as a dispatched `composer create-project`. Run `polka install` inside the new directory to provision its tools.

### `polka config [--env name] <key> <value>`

Creates or updates one environment config value. The default environment is stored in `polka.yaml`; named environments are stored in `polka.<name>.yaml`.

```bash
polka config tools.php 8.4
polka config tools.php-zts 8.4
polka config tools.frankenphp 1.12
polka config tools.apache 2.4
polka config server.type apache
polka config server.type frankenphp
polka config memory-limit 512M
polka config --env blog tools.mysql 8.0
polka config --env blog database.engine mysql
polka config --env reporting tools.postgresql 17
polka config --env reporting database.engine postgresql
polka config --env blog settings.mailpit.smtp-port 1025
polka config --env blog tools.meilisearch 1.48
polka config --env blog settings.meilisearch.port 7700
polka config --env blog tools.traefik 3.3
polka config --env blog settings.traefik.port 8080
```

Use `--env name` to select a named environment. When `--env` is omitted, Polka updates the current environment, falling back to `default` when no local override is selected. Supported keys are schema-aware dot paths such as `tools.php`, `tools.php-zts`, `tools.frankenphp`, `tools.apache`, `tools.meilisearch`, `settings.meilisearch.port`, `settings.meilisearch.master-key`, `tools.traefik`, `settings.traefik.port`, `database.port`, `server.type`, `server.hostname`, `env-vars.APP_ENV`, `memory-limit`, `php-extensions.xdebug`, and `opcache-config.opcache.enable_cli`. `tools.php` and `tools.php-zts` are mutually exclusive; setting one switches the primary runtime and both expose the standard `php` command. `server.type` accepts `php`, `nginx`, `apache`, or `frankenphp`. `tools.pie` is rejected: PIE is managed internally by Polka and driven through `polka ext`. `php-extensions.<vendor>/<name>` keys hold PIE version constraints instead of booleans.

### `polka install [tool:version] [--env name]`

Installs one explicit tool version, or every configured tool version for an environment when no `tool:version` argument is provided.

```bash
polka install
polka install php:8.4
polka install php-zts:8.4
polka install frankenphp:1.12
polka install apache:2.4
polka install --env blog
```

Use `--env name` to select a named environment. When `--env` is omitted, Polka installs against the current environment and prints which one it selected. If no current environment is selected, Polka uses `default` from `polka.yaml`.

For managed tool installation, command shims, PHP runtime generation, and platform support details, see [tools.md](tools.md).

When the environment's `php-extensions` config contains PIE-managed `vendor/name` entries, `polka install` also provisions any of those extensions that the environment's installed PHP does not yet load, using Polka's internal PIE. Fresh checkouts therefore reproduce PIE-managed extensions.

### `polka ext <install|remove> <vendor/name[:version]> [--env name]`

Manages PHP extensions provided by PIE (the PHP Installer for Extensions) for an environment's installed PHP runtime.

```bash
polka ext install xdebug/xdebug
polka ext install xdebug/xdebug:3.4.1
polka ext remove xdebug/xdebug
```

`install` downloads or builds the extension against the environment's standalone `php`/`php-zts` install using Polka's internal PIE, records it under the `php-extensions` config key as `vendor/name: version`, and regenerates the runtime `php.ini`. When no version is given, Polka records the version PIE resolved, falling back to `*` (latest). `remove` uninstalls the extension and deletes its config entry. PIE requires a standalone PHP runtime; it cannot target FrankenPHP's embedded PHP.

### `polka list`

Lists environments discovered from `polka.yaml` and `polka.<name>.yaml` files. The active environment is marked with `*`.

### `polka use <name>`

Selects one environment as the local override. Use `default` to clear the override and fall back to `polka.yaml`.

### `polka status`

Alias: `polka info`

Shows the active environment, prints each configured tool on its own line, includes the resolved web server URL, and reports whether the webserver, phpMyAdmin, Meilisearch, Traefik, managed database, and Mailpit are running.

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
polka exec frankenphp version
polka exec drush status
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

Set `server.type` to `php`, `nginx`, `apache`, or `frankenphp` to select the webserver explicitly. When it is omitted, Polka preserves the existing behavior of selecting nginx when configured and PHP otherwise; Apache is selected only by `server.type: apache`. Nginx and Apache start `php-cgi` on an internal loopback port with generated FastCGI configs. Apache enables project `.htaccess` files by default. FrankenPHP runs through a generated Caddyfile. PHP's built-in webserver uses a generated router that serves existing static files and forwards missing requests into the app router or front controller.

If the environment defines `mailpit`, `meilisearch`, `traefik`, `phpmyadmin`, or a managed database, Polka starts those local services before the webserver. When `traefik` is installed, Polka points it at the webserver and reports the proxy URL (`Reverse proxying through Traefik at ...`). `polka status` prints the full Mailpit, Meilisearch, Traefik, and phpMyAdmin URLs.

HTTPS requires nginx, Apache, or FrankenPHP at start time. They use the generated server certificate from the global Polka cache. The certificate covers `localhost`, `*.localhost`, `127.0.0.1`, and `::1`, and is signed by a generated local Polka CA. Hostnames ending in `.localhost`, such as `blog.localhost`, work without editing the hosts file.

When Polka creates new HTTPS certificate material during `serve`, it prefers to encrypt global-cache private keys with random keys stored in the OS keyring. Webserver processes still receive plaintext PEM runtime copies under `.polka/run` because nginx, Apache, FrankenPHP, and Mailpit require key file paths. If the OS keyring is unavailable, `serve` falls back to the legacy plaintext global-cache private-key format with a warning so local HTTPS remains usable. Existing plaintext keys continue to work.

### `polka stop`

Stops the active environment's background webserver, phpMyAdmin, Meilisearch, Traefik, managed database, and Mailpit when they are running.

### `polka logs <tool> [--level info|error|debug]`

Prints existing log files declared by one managed tool's manifest for the active environment.

```bash
polka logs nginx
polka logs nginx --level error
polka logs apache
polka logs frankenphp
polka logs mailpit
polka logs meilisearch
polka logs mariadb --level error
polka logs postgresql --level error
```

Manifest log paths are resolved under `.polka/run/<tool>/<environment>`. When `--level` is omitted, Polka prints `info`, `error`, and `debug` logs in that order. Missing log files are skipped. If no matching declared log file exists on disk, the command exits with an error. Built-in log declarations cover `nginx`, `apache`, `frankenphp`, `mailpit`, `meilisearch`, `traefik`, `phpmyadmin`, `mysql`, `mariadb`, and `postgresql`.

### `polka cert-install [--no-encryption]`

On Windows or macOS, removes any existing generated Polka CA certificate from the current user's trust store, clears and regenerates the global Polka CA/server certificate pair, then installs the new CA certificate. By default, Polka encrypts the regenerated global-cache private keys with random keys stored in the OS keyring, and the command fails if that keyring storage is unavailable. After certificate generation, unsupported trust-store platforms fail with the certificate path so it can be installed manually.

Use `--no-encryption` to regenerate the private keys in the legacy plaintext PEM format.

## Database Commands

Polka dispatches database commands to the primary database selected by `database.engine` in the current environment. Managed database versions are stored as `tools.mysql`, `tools.mariadb`, or `tools.postgresql`; shared options such as `database.port` live under the root-level `database` section. PostgreSQL defaults to port `5432` and exposes `psql`; MySQL and MariaDB default to `3306`.

### `polka db [args...]`

Forwards arguments to the active environment's database client. Polka injects connection options for the managed local server unless the equivalent native client options are provided. PostgreSQL connections use the generated `.pgpass` through `PGPASSFILE` rather than exposing the managed password in process arguments.

```bash
polka db --version
polka db --execute "show databases"
polka db --db-name app_test --execute "show tables"
polka db --db-name app_test --command "select current_database()"
```

Use `polka db client [args...]` to force client dispatch when the first argument is a reserved Polka database subcommand such as `status`.

### `polka db start`

Initializes the local database data directory if needed and starts the managed database server. PostgreSQL clusters use UTF-8, SCRAM host authentication, a generated `polka` superuser, and a database named after the active environment.

### `polka db stop`

Stops the database server previously started by Polka.

### `polka db status`

Shows whether the managed database server is running and which address it uses.

### `polka db export <path.sql|path.sql.gz>`

Dumps the selected database to a SQL file. Use `--db-name NAME` to target a database other than the default database named after the active environment. PostgreSQL exports are plain SQL without ownership or privilege statements.

### `polka db import <path.sql|path.sql.gz>`

Loads SQL from a `.sql` or `.sql.gz` file into the active database server. Use `--db-name NAME` to target a database other than the default database named after the active environment.
