package cli

const rootUsage = `The polka CLI manages isolated PHP virtual environments.

Usage:
  polka [--root PATH] <command> [options]

Commands:
  init [framework] [--docroot PATH]
                       create the local .polka directory and optional framework config
  new <name>           create an environment with default or explicit versions
  config <key> <value> set one config value for an environment
  install [tool:version]
                       install one tool version or all tools for an environment
  cert-install         install Polka's local HTTPS certificate into the user trust store
  db [args...]         run the active environment's database client or manage its local server
  serve, start [docroot]
                       start the local web server for the active environment
  stop                 stop the active environment's local web server and managed services
  exec <command>       run one command with the local shell environment
  sh, shell            open an interactive shell with local binaries first
  session [start|stop] generate shell scripts that activate or deactivate local binaries
  logs <tool>          print active environment logs for one managed tool
  list                 list environments
  use <name>           select the active environment
  status, info         show the active environment status
  remove <name>        remove an environment
  help                 show this help

Examples:
  polka init
  polka init laravel
  polka new api
  polka config tools.php 8.4
  polka config --env api tools.mysql 8.0
  polka config --env api database.engine mysql
  polka install php:8.4
  polka install --env api
  polka cert-install
  polka db start
  polka db status
  polka db --version
  polka serve public
  polka stop
  polka serve
  polka exec php -v
  polka sh
  polka session start
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
When framework is codeigniter, drupal, wordpress, laravel, or symfony, Polka writes an opinionated default config for that framework.
Framework init is config-only; it does not create app files, install tools, or start services. It fails if polka.yaml already exists.
`

const newUsage = `Usage:
  polka new <name> [--php VERSION] [--composer VERSION] [--nodejs VERSION] [--db-engine mysql|mariadb --db-version VERSION [--db-port PORT]]

Create a new named environment definition in polka.<name>.yaml.
When omitted, --php defaults to 8.4, --composer defaults to 2.8, and --nodejs defaults to 24.
`

const configUsage = `Usage:
  polka config [--env NAME] <key> <value>

Create or update one environment config value. The default environment is stored in polka.yaml; named environments are stored in polka.<name>.yaml.
Use --env NAME to select a named environment. When --env is omitted, Polka updates the current environment, falling back to default when no local override is selected.
Keys are dot-separated YAML paths such as tools.php, database.engine, settings.mailpit.smtp-port, env-vars.APP_ENV, php-extensions.xdebug, and opcache-config.opcache.enable_cli.
`

const installUsage = `Usage:
  polka install [tool:version] [--env NAME]

Install one explicit tool version, such as php:8.4, or install every configured tool version for an environment when no tool argument is provided.
Use --env NAME to select a named environment. When --env is omitted, Polka uses the current environment, falling back to default when no local override is selected.
Polka installs tools from validated global cache payloads when available, otherwise downloads them into the cache first.
`

const certInstallUsage = `Usage:
  polka cert-install

Install Polka's generated local HTTPS certificate into the current user's trust store.
The generated certificate material is global to the Polka cache. This command replaces any existing generated CA/server certificate pair and installs the CA certificate.
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
When the current environment defines a database, mailpit, or phpmyadmin, Polka starts those managed local services first.
Set root-level https to true to serve the webserver and applicable managed services over HTTPS with Polka's generated local certificate.
By default, Polka starts the webserver in the background and returns once it is listening.
Pass --watch to keep the webserver attached to the current terminal with the previous foreground behavior.
`

const stopUsage = `Usage:
  polka stop

Stop the active environment's local web server, phpMyAdmin, managed database, and mailpit when they are running.
`

const execUsage = `Usage:
  polka exec <command> [args...]

Run one command with the same local environment as polka sh.
Polka resolves commands in this order:
1. <root>/bin
2. vendor/bin
3. system PATH
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

const sessionUsage = `Usage:
  polka session start
  polka session stop

Generate scripts that activate or deactivate Polka's local binaries in the current shell.

Preferred project-local wrappers:
  PowerShell: . .\.polka\session-start.ps1
  POSIX:      . ./.polka/session-start

To deactivate the current shell session:
  PowerShell: . .\.polka\session-stop.ps1
  POSIX:      . ./.polka/session-stop

Direct command form is still available:
  PowerShell: . (polka session start)
  POSIX:      . "$(polka session start)"

session start uses the same command resolution order as polka sh:
1. <root>/bin
2. vendor/bin
3. system PATH

On Windows, session start also generates the same temporary vendor/bin .cmd wrappers as polka sh for extensionless Composer PHP proxies and shell launchers with a matching .php source.
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
Also shows whether the active environment's webserver, phpMyAdmin, managed database, and mailpit are currently running.
`

const removeUsage = `Usage:
  polka remove <name>

Remove a named environment definition. The default environment in polka.yaml cannot be removed.
`
