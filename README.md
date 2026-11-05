# Polka

Polka is a CLI tool for managing local PHP development environments. It provides a consistent interface for installing and managing multiple local versions of PHP, Composer, Node.js, and other development tools on a per-project basis. Polka also includes support for serving web applications with nginx, Apache, or FrankenPHP, phpMyAdmin, managed databases, Mailpit, and many more.

## Quick start

From a PHP project directory:

```bash
polka init
polka install
polka exec php -v
```

`polka init` creates `polka.yaml` and local helper scripts, enabling HTTPS and using `<directory>.localhost` as the default server hostname. Add `--docroot PATH` to set the generated default environment's docroot.

Use `polka init drupal`, `polka init laravel`, or `polka init symfony` instead to create a `polka.yaml` with specific framework preset; `cakephp` and `wordpress` are also supported. Framework init does not create app files, install tools, or start services, and it fails when `polka.yaml` already exists.

For native C extensions, `polka init php-extension` writes a build-oriented `polka.yaml` and a PIE-compatible `composer.json` without replacing an existing Composer manifest.

To install a single tool version directly, you can use a `tool:version` argument such as `polka install php:8.4`.

On Linux, PHP, nginx, Apache, and Redis are compiled locally rather than downloaded. Run `polka build php:8.4` once per machine, or let `polka install --build` compile whatever a project is missing. See [Building Tools From Source](docs/tools.md#building-tools-from-source).

Start a local web server when your project has a document root:

```bash
polka start
polka status
polka stop
```

For an interactive shell with `.polka/bin` and `vendor/bin` first on `PATH`, use:

```bash
polka sh
```

For the full command reference, see [docs/commands.md](docs/commands.md). For managed tool behavior, see [docs/tools.md](docs/tools.md). For the package layout and architecture, see [docs/architecture.md](docs/architecture.md).

## Configuration

Polka config files store portable version labels under `tools` and non-version tool options under `settings`. The default environment lives in `polka.yaml`; named environments live in `polka.<name>.yaml`.

The project manifest requires `version: 0.2` (quoted `"0.2"` is also accepted). Older or unversioned manifests are rejected. To regenerate one, delete `polka.yaml` yourself, run `polka init` or `polka init <framework>`, and reapply your settings. Polka does not migrate or delete the old file. Named environment files remain unversioned.

Use `polka config <key> <value>` to update the current environment, or `polka config --env blog <key> <value>` to update a named environment.

Keys are dot-separated config paths such as `tools.php`, `tools.frankenphp`, `tools.apache`, `tools.meilisearch`, `tools.redis`, `tools.rabbitmq`, `server.type`, `database.engine`, `settings.mailpit.smtp-port`, and `settings.redis.port`.

```yaml
# polka.yaml
version: 0.2
root: .polka # state directory for tools, shims, and runtime state
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
  redis: "8.8.0"
  rabbitmq: "4.3"
  traefik: "3.3"
settings:
  phpmyadmin:
    port: 8082
  mailpit:
    smtp-port: 1025
    ui-port: 8025
  meilisearch:
    port: 7700
    master-key: local-dev-key
  redis:
    port: 6379
    password: local-dev-password
  rabbitmq:
    port: 5672
    management-port: 15672
    username: guest
    password: guest
  traefik:
    port: 8080
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
memory-limit: 512M
php-extensions:
  openssl: true
  xdebug: false
# Deprecated compatibility only; prefer PIE vendor/name entries above.
pecl-extensions:
  redis: "6.2.0"
opcache-preset: dev
opcache-config:
  opcache.enable_cli: "1"
```

For per-tool behavior, command shims, Node.js/Yarn support, PostgreSQL notes, PHP-ZTS and FrankenPHP runtime selection, generated `php.ini` behavior, and the `.polka/envs` install layout, see [docs/tools.md](docs/tools.md).

Polka uses the nearest `polka.yaml` in the current directory or its parents. During development you can point a local build at a project elsewhere with `-C`:

```bash
go run . -C ../my-site install
```

The current schema is not yet stable, so expect breaking changes to config file structure and field names until version 1.0.
