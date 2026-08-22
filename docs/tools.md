# Polka Managed Tools

This page describes Polka's managed tool model, command shims, per-tool behavior, PHP runtime generation, and platform download support. For command syntax, see [commands.md](commands.md).

## Tool Config And Shims

Managed tool version labels live under `tools` in `polka.yaml` or `polka.<name>.yaml`. Versionless options, such as Mailpit ports and the phpMyAdmin UI port, live under `settings`.

`polka install [tool:version] [--env name]` installs one explicit tool version, or every configured tool version for an environment when no `tool:version` argument is provided. Polka first checks the global cache metadata and payload checksum, downloads missing or invalid versions into that cache, and materializes the cached payload into the project-local `.polka/envs` layout.

The `.polka/bin` shims mirror the active environment's configured tools. If the current environment does not define a managed tool, Polka removes that local shim instead of leaving a dispatcher that would fail at runtime.

Some configured tool keys expose different command names:

| Tool key | Command shims |
| --- | --- |
| `php` | `php` |
| `php-zts` | `php` |
| `frankenphp` | `frankenphp`, and `php` when no standalone PHP is configured |
| `roadrunner` | `rr` |
| `composer` | `composer` |
| `nodejs` | `node`, `npm`, `npx`, `yarn` |
| `mago` | `mago` |
| `nginx` | `nginx` |
| `apache` | `apache`, `httpd` |
| `mysql` | `mysql` |
| `mariadb` | `mariadb` |
| `postgresql` | `psql` |
| `sqlite` | `sqlite3` |
| `mailpit` | `mailpit` |
| `meilisearch` | `meilisearch` |
| `redis` | `redis-server`, `redis-cli` |
| `rabbitmq` | `rabbitmq-server`, `rabbitmqctl`, `rabbitmq-diagnostics`, `rabbitmq-plugins`, `rabbitmq-queues`, `rabbitmq-streams`, `rabbitmq-upgrade` |
| `traefik` | `traefik` |
| `phpmyadmin` | none |

`nodejs` itself remains config-only. Polka generates `yarn` from Corepack during Node.js install when the Node payload does not already include a Yarn command.

## Install Layout

Polka resolves configured versions against the local install layout under `.polka/envs`:

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
|   |       |-- node[.exe]
|   |       `-- yarn[.cmd]
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
  |-- yarn[.cmd]
  `-- sqlite3[.cmd]
```

## Tool Notes

The `mago` tool key installs the Mago binary and creates a `mago` command shim.

The `frankenphp` tool key installs an official FrankenPHP release and creates `frankenphp` and `php` command shims. FrankenPHP supplies the `php` CLI only when neither `php` nor `php-zts` is configured; a standalone PHP tool always takes precedence. Configuring both is supported, but install, config, and environment-selection commands warn that the CLI and FrankenPHP server runtimes may differ. FrankenPHP is selected as the webserver only by `server.type: frankenphp`. On Linux, the managed `php` command invokes `frankenphp php-cli`. `polka install` writes a generated `php.ini` for FrankenPHP using the environment's effective framework extensions, user extension overrides, and OPcache settings; Polka supplies it through `PHPRC` for serving and dispatched PHP or FrankenPHP commands.

The `roadrunner` tool key installs an official [RoadRunner](https://roadrunner.dev/) release and creates an `rr` command shim. Unlike FrankenPHP, RoadRunner does not bundle a PHP runtime and Polka does not generate its configuration: it is an installable, dispatchable tool only, not a selectable `server.type`. Supply your own `.rr.yaml` and PSR worker script (for example via `spiral/roadrunner-http`) and run it through the shim, such as `polka rr serve`. RoadRunner's HTTP plugin serves an application-provided worker rather than a generic front controller, so it has no auto-generated serve runtime.

### PHP Build Tools

Set `settings.php.extension-sdk: true` to make `polka install` prepare the native PHP extension toolchain for the configured `php` or `php-zts` runtime. On Windows amd64, Polka downloads the exact PHP devel pack matching the runtime's resolved version, TS/NTS mode, architecture, and compiler, plus the pinned `php-sdk-binary-tools` release. These are internal dependencies under `.polka/envs/php-devel/<php-version>` and `.polka/envs/php-sdk/<sdk-version>`; they cannot be configured under `tools` and do not create shims. `polka sh` and `polka exec` add both directories to `PATH`, so commands such as `polka exec phpize --version` and `polka exec phpsdk-vs17-x64.bat` resolve after installation.

Polka does not install Microsoft Visual Studio Build Tools; a compatible MSVC toolchain remains a Windows host prerequisite. On Linux, Polka downloads no SDK payload. Instead, install verifies `php`, matching `phpize` and `php-config`, `make`, `autoconf`, and either `cc` or `gcc` on the host, and fails with the missing command named in the error. Install the distribution's PHP development package matching the selected PHP plus the standard C build toolchain before retrying.

PIE is an internal-only tool: it cannot be configured under `tools`, has no command shim, and is provisioned into the global internal tools directory on demand. `polka ext install|remove` and the `polka install` extension provisioning drive PIE against the environment's standalone PHP runtime. See the Internal Tools section below.

The `sqlite` tool key installs SQLite's command-line tools, creates a `sqlite3` command shim, and enables `pdo_sqlite` and `sqlite3` for configured PHP runtimes.

The `postgresql` tool key installs PostgreSQL, creates a `psql` command shim, and enables `pgsql` and `pdo_pgsql` for configured PHP runtimes. Windows uses EnterpriseDB's portable binaries; Linux amd64 uses the corresponding portable embedded PostgreSQL archive published through Maven Central because current EnterpriseDB releases no longer provide Linux binary archives.

The `phpmyadmin` tool key installs the phpMyAdmin web app archive under `.polka/envs/phpmyadmin/<version>`, writes a generated `config.inc.php` with a fresh `blowfish_secret`, and uses managed MySQL/MariaDB credentials to skip the phpMyAdmin login screen when one of those managed databases is configured. With PostgreSQL, Polka warns because phpMyAdmin only supports MySQL/MariaDB, then starts phpMyAdmin without managed PostgreSQL login or storage integration. Its UI `port` setting lives under `settings.phpmyadmin`. It inherits HTTPS and the selected nginx or FrankenPHP HTTPS provider from the environment. It does not create a command shim.

The `mailpit` tool key installs Mailpit and creates a `mailpit` command shim. Its SMTP and UI port settings live under `settings.mailpit`.

The `meilisearch` tool key installs Meilisearch and creates a `meilisearch` command shim. Its HTTP port and optional local master key live under `settings.meilisearch`; Polka stores only a runtime fingerprint of the master key and does not print it in status output.

The `redis` tool key installs Redis and creates `redis-server` and `redis-cli` command shims. Its port and optional local password live under `settings.redis`; the password maps to Redis `requirepass`, and Polka stores only a runtime fingerprint of it and does not print it in status output. On Windows, Redis is provided by a community native build (`zkteco-home/redis-windows`), whose prebuilt binaries are committed to the repository rather than published as release assets, so Polka downloads the tag's GitHub source-archive zip. Because that archive has no published checksum, Polka verifies integrity by comparing the source commit embedded in the zip's archive comment against the commit the tag resolves to via the GitHub API.

The `rabbitmq` tool key installs the official RabbitMQ Windows ZIP and automatically provisions a project-local Erlang/OTP 27 dependency. RabbitMQ is Windows amd64-only in this release. AMQP, management UI, and credential settings live under `settings.rabbitmq`; defaults are `5672`, `15672`, and `guest`/`guest`. Polka binds both listeners to localhost, enables `rabbitmq_management`, keeps broker data per environment, and never prints or stores the plaintext password in runtime state.

The `traefik` tool key installs the [Traefik](https://traefik.io/) reverse proxy and creates a `traefik` command shim. Polka runs it as a managed background service that **fronts the environment webserver**: when `tools.traefik` is installed, `polka serve` starts Traefik on a `web` entrypoint at `settings.traefik.port` (default `8080`) and points it at whatever webserver the environment serves (php, nginx, apache, or frankenphp). Traefik uses a file provider that watches `.polka/run/traefik/<environment>/dynamic`; once the webserver address is known, Polka writes the routing rules there (`polka.yml`) and Traefik hot-reloads them, so the site is reachable through Traefik at `<scheme>://<hostname>:<port>` in addition to the webserver's own address.

When the environment enables `https`, Traefik terminates TLS at its entrypoint with Polka's local certificate (the same local CA used by the other webservers), so the proxy URL is `https://<hostname>:<port>` and is trusted once the CA is installed; the private key is materialized under `.polka/run/traefik/<environment>/`, and the upstream connection to the webserver skips certificate verification for the local self-signed certificate. If `tools.traefik` is configured but not installed, `polka serve` proceeds without the proxy.

## Internal Tools

Some tools are implementation details of Polka itself rather than project dependencies. Internal tools reuse the shared download cache but are installed once per machine into the global internal tools directory instead of `.polka/envs`: `POLKA_TOOLS_DIR` when set, otherwise `<POLKA_CACHE_DIR>/internal-tools` when the cache override is set, otherwise `<user cache dir>/polka/tools`. They are never shimmed or dispatchable.

The reserved `_internal` environment tracks internal tool versions in `<tools dir>/polka._internal.yaml` (standard environment-file shape). Polka seeds it from the same default tool versions `polka new` uses on first use and users may edit the file to override internal tool versions. The name `_internal` starts with an underscore, which project environment names reject, so it can never collide.

Any tool can be installed internally when a Polka command needs it: `polka create-project` provisions an internal PHP runtime and composer, `polka ext` provisions internal PHP plus PIE, RabbitMQ pulls in Erlang, and the PHP extension SDK pulls in PHP development payloads. The internal PHP enables a curated broad set of common extensions (TLS, intl, gd, sodium, fileinfo, the PDO drivers, and more), filtered to those actually bundled with the installed PHP build, so composer scaffolders and PIE cover most use cases out of the box. Tools whose manifest declares `internal-only: true` additionally cannot be configured in project environments or exposed as normal dispatch shims; `tools.pie` fails validation with a message pointing to `polka ext`.

## PHP Runtime Generation

When an environment defines `memory-limit`, `php-extensions`, configures a tool with PHP dependencies, selects `opcache-preset` or `opcache-config`, or uses a framework with generated PHP defaults, `polka install` writes a generated `php.ini` next to each configured PHP, PHP-ZTS, and FrankenPHP runtime.

`memory-limit` sets PHP's `memory_limit` directive and accepts values such as `512M`, `1G`, raw bytes, or `-1` for unlimited memory. Extensions are merged in this order: framework requirements, every configured tool's requirements, then user-defined `php-extensions`. Explicit `false` values therefore disable framework or tool defaults. MySQL and MariaDB enable `mysqli` and `pdo_mysql`; PostgreSQL enables `pgsql` and `pdo_pgsql`; SQLite enables `pdo_sqlite` and `sqlite3`; Composer enables `openssl` and `zip`.

`php-extensions` keys containing a slash, such as `xdebug/xdebug: "3.4.1"`, are PIE-managed extensions: the value is a Composer version constraint instead of a boolean. `polka ext install` writes these entries, `polka install` provisions their binaries through the internal PIE, and the generated `php.ini` loads the module named after the package (the part after the slash). Known Zend extensions (currently `opcache` and `xdebug`) load through `zend_extension` directives; everything else loads through `extension`. An explicit boolean toggle for the same module name wins, so `xdebug: false` disables a PIE-installed Xdebug without uninstalling it.

`pecl-extensions` provides discouraged legacy compatibility for packages that have not migrated to PIE. Scalar values pin releases; object values add persisted `configure-options`. Windows selects an official DLL matching PHP version and TS/NTS mode. Linux builds against matching `phpize` and `php-config` using the existing host compiler toolchain. Polka tracks installed artifacts in the PHP runtime directory so removal does not delete modified or shared files. Prefer PIE whenever possible because PECL is deprecated.

Before writing `php.ini`, Polka checks `php -nm` and skips extensions that are already built into that PHP binary. If the effective extension config enables `curl` or `openssl`, Polka also configures `curl.cainfo` and `openssl.cafile` with a CA bundle. Install also ensures each managed PHP runtime has a local `extras/ssl/openssl.cnf`; managed PHP commands set `OPENSSL_CONF` to that file for OpenSSL key and CSR generation.

`opcache-preset` may be omitted, `none`, `dev`, or `production`. `dev` enables OPcache with timestamp validation and immediate revalidation. `production` enables OPcache with timestamp validation disabled. `opcache-config` accepts `opcache.*` directives and is applied over the preset; framework-provided directives, such as Drupal's `opcache.save_comments = 1`, sit between the preset and user config. Re-run `polka install` after changing PHP extension, memory limit, or OPcache settings.

## Runtime Environment

Polka composes runtime environment variables for the active environment from five sources in this precedence order, lowest to highest:

1. inherited process environment
2. project `.env`
3. current environment file's `env-file`
4. framework-provided database variables when generated managed database credentials exist
5. current environment file's `env-vars`

The `.env` file is loaded automatically from the directory containing `polka.yaml` when present. `env-file` paths are resolved relative to that same directory unless absolute, and `env-vars` always win when keys overlap. This runtime environment applies to `polka sh`, `polka exec`, `polka serve`, generated `.polka/bin` dispatch shims, and database client/import/export commands.

## Composer Hooks

After dispatched `composer install`, `composer update`, or `composer create-project` exits with any code except Composer's dependency solver failure code 2, the active framework plugin may create or update framework-local secret files from Polka-managed database credentials. The built-in CakePHP hook updates `config/app_local.php`, CodeIgniter updates the app `.env` database settings, Drupal writes `settings.polka.php` and includes it from `settings.php`, WordPress updates `wp-config.php` DB constants, Laravel updates the app `.env` DB settings, and Symfony writes `DATABASE_URL` to `.env.local`.

## Platform Notes

Automatic nginx downloads are currently implemented on Windows amd64.

Automatic Apache downloads use Apache Lounge builds and are currently implemented on Windows amd64.

Automatic Mailpit downloads are currently implemented for Windows amd64 and Linux amd64.

Automatic FrankenPHP downloads are currently implemented for Windows amd64 and Linux amd64.

Automatic RoadRunner downloads are currently implemented for Windows amd64 and Linux amd64.

Automatic phpMyAdmin downloads use the official cross-platform zip archive.
