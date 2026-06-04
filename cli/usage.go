package cli

const rootUsage = `The polka CLI manages isolated PHP virtual environments.

Usage:
  polka [--root PATH] <command> [options]

Commands:
  init                 create the local .polka directory and sync the active shims
  new <name>           create an environment with default or explicit versions
  config [name]        set php, composer, nodejs, and database settings for an environment
  install [name]       install all tools for an environment
  cert-install         install Polka's local HTTPS certificate into the user trust store
  db [args...]         run the active environment's database client or manage its local server
  serve, start [docroot]
                       start the local web server for the active environment
  stop                 stop the active environment's local web server and managed services
  exec <command>       run one command with the local shell environment
  sh, shell            open an interactive shell with local binaries first
  session [start|stop] generate shell scripts that activate or deactivate local binaries
  list                 list environments
  use <name>           select the active environment
  status, info         show the active environment status
  remove <name>        remove an environment
  help                 show this help

Examples:
  polka init
  polka new api
  polka config api --php 8.4 --composer 2.8 --nodejs 24
  polka config api --db-engine mysql --db-version 8.0 --db-port 3306
  polka install api
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
  polka list
  polka use api
  polka status

Flags:
  --root PATH          override the state directory (defaults to ./.polka)
`

const initUsage = `Usage:
  polka init

Create the local .polka directory, bootstrap polka.yaml, and sync the active environment's dispatch shims into .polka/bin.
`

const newUsage = `Usage:
  polka new <name> [--php VERSION] [--composer VERSION] [--nodejs VERSION] [--db-engine mysql|mariadb --db-version VERSION [--db-port PORT]]

Create a new named environment definition in polka.<name>.yaml.
When omitted, --php defaults to 8.4, --composer defaults to 2.8, and --nodejs defaults to 24.
`

const configUsage = `Usage:
  polka config [name] [--php VERSION] [--composer VERSION] [--nodejs VERSION] [--db-engine mysql|mariadb --db-version VERSION [--db-port PORT]]

Create or update an environment definition. The default environment is stored in polka.yaml; named environments are stored in polka.<name>.yaml.
When name is omitted, Polka updates the current environment, falling back to default when no local override is selected.
`

const installUsage = `Usage:
   polka install [name]

Install all configured tool versions for an environment.
When name is omitted, Polka installs the current environment, falling back to default when no local override is selected.
Polka copies them from the global cache when available, otherwise downloads them into the cache first.
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
When --server is omitted, Polka uses the current environment's server.hostname and server.port, defaulting to localhost:8000.
When the current environment defines a database, mailpit, or phpmyadmin, Polka starts those managed local services first.
Set mailpit.https to true to serve the Mailpit UI over HTTPS and enable SMTP STARTTLS with Polka's generated local certificate.
Set phpmyadmin.https to true to serve phpMyAdmin over HTTPS through nginx with Polka's generated local certificate.
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
