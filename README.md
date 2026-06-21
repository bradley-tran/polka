# Polka

Polka is a CLI tool for managing PHP virtual environments. It provides a consistent interface for installing and managing multiple local versions of PHP, Composer, Node.js, and other development tools on a per-project basis. Polka also includes support for serving web applications with nginx or FrankenPHP, phpMyAdmin, managed databases, and Mailpit.

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

Use `polka config <key> <value>` to update the current environment, or `polka config --env blog <key> <value>` to update a named environment. Keys are dot-separated config paths such as `tools.php`, `tools.php-zts`, `tools.frankenphp`, `server.type`, `database.engine`, and `settings.mailpit.smtp-port`.

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
  sqlite: "3.53"
  mariadb: "11.8"
  phpmyadmin: 5.2
  mailpit: "1.30"
settings:
  phpmyadmin:
    port: 8082
  mailpit:
    smtp-port: 1025
    ui-port: 8025
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

`server.type` accepts `php`, `nginx`, or `frankenphp`. When omitted, existing behavior is preserved: nginx is selected when configured, otherwise PHP's built-in server is used. FrankenPHP exposes both `frankenphp` and `php`: its bundled CLI supplies `php` when neither `php` nor `php-zts` is configured, while either standalone tool takes precedence when present. Composer, PIE, the PHP webserver, phpMyAdmin, extension settings, and OPcache settings use the same selected CLI provider. nginx still requires standalone `php`/`php-zts` because it needs `php-cgi`. When both FrankenPHP and a standalone PHP tool are configured, install and shim-refreshing commands warn that the CLI and FrankenPHP server runtimes may differ. Polka mirrors the effective framework, extension, and OPcache settings into the bundled FrankenPHP runtime and supplies its generated `php.ini` through `PHPRC`. Both nginx and FrankenPHP reuse Polka's generated HTTPS certificate.

Polka resolves those versions against the local install layout under `.polka/envs`:

Framework presets apply their default PHP extensions during install. User-defined `php-extensions` entries override those defaults, including `false` values that disable an extension. `opcache-preset` accepts `none`, `dev`, or `production`; `opcache-config` accepts `opcache.*` directives applied over the preset and any framework defaults. Re-run `polka install` after changing PHP extension or OPcache settings so Polka can regenerate `php.ini`.

```text
.polka/
|-- envs/
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
  |-- frankenphp[.cmd]
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
