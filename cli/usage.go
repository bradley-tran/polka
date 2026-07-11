package cli

const rootUsage = `The polka CLI manages isolated PHP virtual environments.

Usage:
  polka [--root PATH] <command> [options]

Commands:
  init [framework] [--docroot PATH]
                       create the local .polka directory and optional framework config
  create-project <package> [directory]
                       scaffold a new app with Polka's internal composer and initialize polka in it
  env <command>        manage environment definitions (new, config, install, list, use, remove)
  new <name>           create an environment with default or explicit versions
  config <key> <value> set one config value for an environment
  ext <install|remove> <vendor/name[:version]|name[:version]>
                       manage recommended PIE or legacy PECL PHP extensions
  install [tool:version]
                       install one tool version or all tools for an environment
  php [args...]        run php with the local shell environment
  composer [args...]   run composer with the local shell environment
  cert-install         install Polka's local HTTPS certificate into the user trust store
  db [args...]         run the active environment's database client or manage its local server
  serve, start [docroot]
                       start the local web server for the active environment
  stop                 stop the active environment's local web server and managed services
  exec <command>       run one command with the local shell environment
  sh, shell            open an interactive shell with local binaries first
  logs <tool>          print active environment logs for one managed tool
  list                 list environments
  use <name>           select the active environment
  status, info         show the active environment status
  remove <name>        remove an environment
  help                 show this help

Examples:
  polka init
  polka init laravel
  polka create-project laravel/laravel demo
  polka ext install xdebug/xdebug
  polka new api
  polka config tools.php 8.4
  polka config tools.php-zts 8.4
  polka config tools.frankenphp 1.12
  polka config tools.apache 2.4
  polka config server.type apache
  polka config server.type frankenphp
  polka config --env api tools.mysql 8.0
  polka config --env api database.engine mysql
  polka install php:8.4
  polka install php-zts:8.4
  polka install frankenphp:1.12
  polka install --env api
  polka env install
  polka php -v
  polka composer install
  polka cert-install
  polka db start
  polka db status
  polka db --version
  polka serve public
  polka stop
  polka serve
  polka exec php -v
  polka sh
  polka logs nginx
  polka logs nginx --level error
  polka list
  polka use api
  polka status

Flags:
  --root PATH          override the state directory (defaults to ./.polka)
`

const initUsage = `Usage:
  polka init [framework] [--docroot PATH]

Create the local .polka directory, bootstrap polka.yaml, and sync the active environment's dispatch shims into .polka/bin.
The generated default environment enables HTTPS and uses <directory>.localhost as server.hostname.
Use --docroot PATH to set the generated default environment's document root.
When framework is cakephp, codeigniter, drupal, wordpress, laravel, or symfony, Polka writes an opinionated default config for that framework.
Framework init is config-only; it does not create app files, install tools, or start services. It fails if polka.yaml already exists.
`

const envUsage = `Usage:
  polka env <command> [options]

Manage environment definitions. The default environment is stored in polka.yaml; named environments are stored in polka.<name>.yaml.

Commands:
  new <name>           create an environment with default or explicit versions
  config <key> <value> set one config value for an environment
  install [tool:version]
                       install one tool version or all tools for an environment
  list                 list environments
  use <name>           select the active environment
  remove <name>        remove an environment

Each of these is also available as a top-level shortcut, so "polka install" and "polka env install" are equivalent.
`

const createProjectUsage = `Usage:
  polka create-project <package> [directory] [composer-args...]

Scaffold a new application with Polka's internal composer, then initialize polka in the created directory.
Polka provisions an internal PHP runtime and composer into the global tools directory on first use; project configs never reference them.
Remaining arguments pass through to composer create-project unchanged, so options such as --stability work as usual.
After scaffolding, Polka detects the framework (cakephp, codeigniter, drupal, wordpress, laravel, or symfony) from the package name or marker files and writes the matching polka.yaml preset; run polka install inside the new directory to provision its tools.
`

const extUsage = `Usage:
  polka ext install <vendor/name[:version]> [--env NAME]
  polka ext remove <vendor/name> [--env NAME]
  polka ext install <pecl-name[:version]> [--configure-option NAME=VALUE] [--clear-configure-options] [--env NAME]
  polka ext remove <pecl-name> [--env NAME]

Package names containing a slash use PIE (the recommended PHP Installer for Extensions) and are recorded under php-extensions.
Bare package names use deprecated PECL compatibility support and are recorded under pecl-extensions. Prefer PIE whenever an extension publishes a PIE package.
PECL uses matching prebuilt DLLs on Windows and a local phpize/php-config/compiler toolchain on Linux. Configure options are persisted for reproducible Linux builds.
Both providers are reprovisioned by polka install and require standalone php or php-zts; neither can target FrankenPHP's embedded PHP.
`

const newUsage = `Usage:
  polka new <name> [--php VERSION] [--composer VERSION] [--nodejs VERSION] [--db-engine mysql|mariadb|postgresql --db-version VERSION [--db-port PORT]]

Create a new named environment definition in polka.<name>.yaml.
When omitted, --php defaults to 8.4, --composer defaults to 2.8, and --nodejs defaults to 24.
`

const configUsage = `Usage:
  polka config [--env NAME] <key> <value>

Create or update one environment config value. The default environment is stored in polka.yaml; named environments are stored in polka.<name>.yaml.
Use --env NAME to select a named environment. When --env is omitted, Polka updates the current environment, falling back to default when no local override is selected.
Keys are dot-separated YAML paths such as tools.php, tools.php-zts, tools.frankenphp, tools.apache, tools.meilisearch, tools.redis, server.type, database.engine, settings.mailpit.smtp-port, settings.meilisearch.port, settings.meilisearch.master-key, settings.redis.port, settings.redis.password, env-vars.APP_ENV, memory-limit, php-extensions.xdebug, pecl-extensions.redis, and opcache-config.opcache.enable_cli.
tools.php and tools.php-zts are mutually exclusive standalone runtimes; setting one clears the other and both take precedence for the php command. FrankenPHP supplies php when neither is configured.
server.type accepts php, nginx, apache, or frankenphp. When omitted, Polka preserves the legacy behavior of selecting nginx when configured and PHP otherwise.
`

const installUsage = `Usage:
  polka install [tool:version] [--env NAME] [--force]

Install one explicit tool version, such as php:8.4, php-zts:8.4, or frankenphp:1.12, or install every configured tool version for an environment when no tool argument is provided.
Use --env NAME to select a named environment. When --env is omitted, Polka uses the current environment, falling back to default when no local override is selected.
Polka installs tools from validated global cache payloads when available, otherwise downloads them into the cache first.
Tools already installed at the requested version are skipped and reported as unchanged; pass --force to reinstall them. Installing an explicit tool:version always reinstalls that tool, and its version is only written to the config file after the install succeeds.
When FrankenPHP and standalone PHP are both configured, Polka warns that the php CLI and FrankenPHP server runtimes may differ.
`

const certInstallUsage = `Usage:
  polka cert-install [--no-encryption]

Install Polka's generated local HTTPS certificate into the current user's trust store.
The generated certificate material is global to the Polka cache. On supported platforms, this command removes any existing generated CA certificate from the trust store, replaces the generated CA/server certificate pair, and installs the new CA certificate.
By default, Polka encrypts the generated global-cache private keys with random keys stored in the OS keyring.
Use --no-encryption to write the private keys in the legacy plaintext PEM format.
`

const dbUsage = `Usage:
  polka db [--db-name NAME] [args...]
  polka db client [--db-name NAME] [args...]
  polka db export [--db-name NAME] <path.sql|path.sql.gz>
  polka db import [--db-name NAME] <path.sql|path.sql.gz>
  polka db start
  polka db stop
  polka db status

Run or manage the active environment's configured database tool.
Polka dispatches to the primary database selected by the current environment's database.engine setting.

Subcommands:
  client   force client dispatch, even for reserved words such as status
  export   dump the selected database to a .sql or .sql.gz file
  import   load SQL from a .sql or .sql.gz file into the active database server
  start    initialize the local data directory if needed and start the database server
  stop     stop the database server previously started by Polka
  status   show whether the managed database server is running

Polka creates and targets a default database named after the active environment.
Pass --db-name NAME to target a different database for a single db, client, import, or export command.

When no subcommand is provided, Polka forwards the arguments to the database client.
`

const startUsage = `Usage:
  polka serve [docroot] [--server HOST:PORT] [--watch]
  polka start [docroot] [--server HOST:PORT] [--watch]

Start the active environment's local web server.
When docroot is omitted, Polka uses docroot from the current environment file.
When --server is omitted, Polka uses the current environment's server.hostname, server.port, and root-level https setting, defaulting to localhost:8000.
server.type explicitly selects php, nginx, apache, or frankenphp. When omitted, nginx is selected when configured and PHP is used otherwise.
When the current environment defines a database, mailpit, meilisearch, redis, or phpmyadmin, Polka starts those managed local services first.
Set root-level https to true to serve nginx, Apache, or FrankenPHP and applicable managed services over HTTPS with Polka's generated local certificate. PHP's built-in webserver does not support HTTPS.
By default, Polka starts the webserver in the background and returns once it is listening.
Pass --watch to keep the webserver attached to the current terminal with the previous foreground behavior.
`

const stopUsage = `Usage:
  polka stop

Stop the active environment's local web server, phpMyAdmin, Meilisearch, Redis, Traefik, managed database, and mailpit when they are running.
`

const execUsage = `Usage:
  polka exec <command> [args...]

Run one command with the same local environment as polka sh.
Polka resolves commands in this order:
1. <root>/bin
2. vendor/bin
3. system PATH

On Windows, direct extensionless PHP-shebang commands in Composer scripts run through the environment's managed PHP.
`

const shUsage = `Usage:
  polka sh
  polka shell

Open an interactive shell with command resolution in this order:
1. <root>/bin
2. vendor/bin
3. system PATH

On Windows, Polka launches PowerShell.
When vendor/bin contains extensionless Composer PHP proxies or shell launchers with a matching .php source, Polka generates temporary .cmd wrappers for those commands on Windows and runs the PHP target under the local php CLI.
On POSIX systems, Polka launches $SHELL when it is set, otherwise /bin/sh.
`

const logsUsage = `Usage:
  polka logs <tool> [--level info|error|debug]

Print existing log files declared by a managed tool's manifest for the active environment.
Manifest log paths are resolved under <root>/run/<tool>/<environment>.
When --level is omitted, Polka prints info, error, and debug logs in that order.
Missing log files are skipped. If no matching declared log file exists on disk, the command exits with an error.
`

const listUsage = `Usage:
  polka list

List the environments configured by Polka config files.
The default environment is polka.yaml; named environments are polka.<name>.yaml.
`

const useUsage = `Usage:
  polka use <name>

Select one of the environments defined by polka.yaml or polka.<name>.yaml.
Use default to clear the local override and fall back to polka.yaml.
`

const statusUsage = `Usage:
  polka status
  polka info

Show the active environment, including one line per configured tool and the resolved server URL.
The active environment name is stored in .polka/run/current when a local override is selected; otherwise Polka uses default.
Also shows whether the active environment's webserver, phpMyAdmin, Meilisearch, Redis, Traefik, managed database, and mailpit are currently running.
`

const removeUsage = `Usage:
  polka remove <name>

Remove a named environment definition. The default environment in polka.yaml cannot be removed.
`
