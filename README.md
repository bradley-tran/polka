# Polka

Polka is a CLI tool for managing PHP virtual environments. It provides a consistent interface for installing and managing multiple local versions of PHP, Composer, Node.js, and other development tools on a per-project basis. Polka also includes support for serving web applications with nginx, Apache, or FrankenPHP, phpMyAdmin, managed databases, and Mailpit.

## Quick start

From a PHP project directory:

```bash
polka init
polka new blog
polka install --env blog
polka use blog
polka exec php -v
```

`polka init` creates `polka.yaml` and local helper scripts, enabling HTTPS and using `<directory>.localhost` as the default server hostname. Add `--docroot PATH` to set the generated default environment's docroot. Use `polka init cakephp`, `polka init laravel`, `polka init symfony`, or `polka init codeigniter` instead to write a config-only framework preset; `drupal` and `wordpress` are also supported. Framework init does not create app files, install tools, or start services, and it fails when `polka.yaml` already exists.

The remaining commands add a named `blog` environment in `polka.blog.yaml`, install its configured tools into `.polka/envs`, select it as the active environment, and run PHP through Polka's local command resolution. To install a single tool version directly, use a `tool:version` argument such as `polka install php:8.4`.

Start a local web server when your project has a document root:

```bash
polka serve public
polka status
polka stop
```

For an interactive shell with `.polka/bin` and `vendor/bin` first on `PATH`, use:

```bash
polka sh
```

For the full command reference, see [docs/commands.md](docs/commands.md). For the package layout and architecture, see [docs/architecture.md](docs/architecture.md).

## Configuration

Polka config files store portable version labels under `tools` and non-version tool options under `settings`. The default environment lives in `polka.yaml`; named environments live in `polka.<name>.yaml`.

Use `polka config <key> <value>` to update the current environment, or `polka config --env blog <key> <value>` to update a named environment. Keys are dot-separated config paths such as `tools.php`, `tools.php-zts`, `tools.frankenphp`, `tools.apache`, `tools.meilisearch`, `server.type`, `database.engine`, `settings.mailpit.smtp-port`, and `settings.meilisearch.port`.

```yaml
# polka.yaml
version: 0.1
root: .polka
framework: laravel
https: true
tools:
  php: 8.4
  composer: 2.8
  pie: 1.4
  nodejs: 24
  mago: "1.27"
  nginx: 1.30
  apache: "2.4"
  sqlite: "3.53"
  mariadb: "11.8"
  phpmyadmin: 5.2
  mailpit: "1.30"
  meilisearch: "1.48"
settings:
  phpmyadmin:
    port: 8082
  mailpit:
    smtp-port: 1025
    ui-port: 8025
  meilisearch:
    port: 7700
    master-key: local-dev-key
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
php-extensions:
  openssl: true
  xdebug: false
opcache-preset: dev
opcache-config:
  opcache.enable_cli: "1"
```

PostgreSQL is available as a managed alternative on Windows and Linux amd64. Its version label may be a major release such as `17`; Polka resolves that label to the newest available minor release while keeping the configured label stable.

```yaml
tools:
  postgresql: "17"
database:
  engine: postgresql
  port: 5432
```

PostgreSQL exposes the `psql` shim and is supported by the CakePHP, CodeIgniter, Drupal, Laravel, and Symfony integrations. WordPress is MySQL-family only, so Polka rejects WordPress with PostgreSQL. phpMyAdmin can still be configured, but Polka warns that it only supports MySQL/MariaDB and starts it without managed PostgreSQL login or storage integration.

```yaml
# polka.legacy.yaml
tools:
  php: 8.2
  composer: 2.6
```

Use `php-zts` instead of `php` when the environment requires a Thread Safe PHP build:

```yaml
tools:
  php-zts: 8.4
  composer: 2.8
```

`php` and `php-zts` are mutually exclusive standalone runtimes. Both provide the standard `php` command and are used by Composer, PIE, serving, phpMyAdmin, extensions, and OPcache configuration. Setting or explicitly installing one runtime clears the other. Automatic PHP downloads remain Windows-only; `php` selects only NTS archives and `php-zts` selects only TS archives.

To serve with FrankenPHP, configure its release version and select it explicitly:

```yaml
tools:
  php: 8.4
  composer: 2.8
  frankenphp: "1.12"
server:
  type: frankenphp
  hostname: blog.localhost
  port: 8443
```

To serve with Apache, configure its release version and select it explicitly:

```yaml
tools:
  php: 8.4
  apache: "2.4"
server:
  type: apache
  hostname: blog.localhost
  port: 8443
```

`server.type` accepts `php`, `nginx`, `apache`, or `frankenphp`. When omitted, existing behavior is preserved: nginx is selected when configured, otherwise PHP's built-in server is used. Apache is selected only when `server.type: apache` is set. FrankenPHP exposes both `frankenphp` and `php`: its bundled CLI supplies `php` when neither `php` nor `php-zts` is configured, while either standalone tool takes precedence when present. Composer, PIE, the PHP webserver, phpMyAdmin, extension settings, and OPcache settings use the same selected CLI provider. nginx and Apache require standalone `php`/`php-zts` because they need `php-cgi`. When both FrankenPHP and a standalone PHP tool are configured, install and shim-refreshing commands warn that the CLI and FrankenPHP server runtimes may differ. Polka mirrors the effective framework, extension, and OPcache settings into the bundled FrankenPHP runtime and supplies its generated `php.ini` through `PHPRC`. nginx, Apache, and FrankenPHP reuse Polka's generated HTTPS certificate.

Polka resolves those versions against the local install layout under `.polka/envs`:

Framework presets and configured tools apply their required PHP extensions during install. Tool requirements include database drivers and Composer's `openssl` and `zip` extensions. User-defined `php-extensions` entries override those defaults, including `false` values that disable an extension. `opcache-preset` accepts `none`, `dev`, or `production`; `opcache-config` accepts `opcache.*` directives applied over the preset and any framework defaults. Re-run `polka install` after changing PHP extension or OPcache settings so Polka can regenerate `php.ini`.

```text
.polka/
|-- envs/
|   |-- apache/
|   |   `-- 2.4/
|   |       `-- bin/httpd[.exe]
|   |-- composer/
|   |   `-- 2.8/
|   |       `-- bin/composer[.cmd|.bat|.exe|.phar]
|   |-- frankenphp/
|   |   `-- 1.12/
|   |       |-- frankenphp[.exe]
|   |       `-- php[.exe|.cmd]
|   |-- nodejs/
|   |   `-- 24/
|   |       `-- node[.exe]
|   |-- phpmyadmin/
|   |   `-- 5.2/
|   |       `-- index.php
|   |-- php/
|   |   `-- 8.4/
|   |       `-- bin/php[.exe|.cmd|.bat]
|   |-- php-zts/
|   |   `-- 8.4/
|   |       `-- bin/php[.exe|.cmd|.bat]
|   `-- sqlite/
|       `-- 3.53/
|           `-- sqlite3[.exe]
`-- bin/
  |-- apache[.cmd]
  |-- frankenphp[.cmd]
  |-- httpd[.cmd]
  |-- node[.cmd]
  |-- npm[.cmd]
  |-- npx[.cmd]
  `-- sqlite3[.cmd]
```

During development you can override the state directory while keeping the config file next to it:

```bash
go run . --root ./.polka-dev init
```

The current schema is not yet stable, so expect breaking changes to config file structure and field names until version 1.0.
