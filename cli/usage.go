package cli

const rootUsage = `Polka manages isolated PHP virtual environments.

Usage:
  polka [--root PATH] <command> [options]

Commands:
  init                 create the local .polka directory and sync the active shims
	new <name>           create an environment with default or explicit versions
  config [name]        set php, composer, and database settings for an environment
  install [name]       install all tools for an environment
  db [args...]         run the active environment's database client or manage its local server
	serve <docroot>      start the PHP local server for the active environment
  list                 list environments
  use <name>           mark an environment as current
  current              show the active environment
  remove <name>        remove an environment
  help                 show this help

Examples:
  polka init
	polka new api
	polka config api --php 8.4 --composer 2.8
  polka config api --db-engine mysql --db-version 8.0 --db-port 3306
	polka install api
  polka db start
  polka db status
  polka db --version
	polka serve public
  polka list
  polka use api
  polka current

Flags:
	--root PATH          override the state directory (defaults to ./.polka)
`

const initUsage = `Usage:
  polka init

Create the local .polka directory, bootstrap polka.yaml, and sync the active environment's dispatch shims into .polka/bin.
`

const newUsage = `Usage:
  polka new <name> [--php VERSION] [--composer VERSION] [--db-engine mysql|mariadb --db-version VERSION [--db-port PORT]]

Create a new environment definition in polka.yaml.
When omitted, --php defaults to 8.4 and --composer defaults to 2.8.
`

const configUsage = `Usage:
  polka config [name] [--php VERSION] [--composer VERSION] [--db-engine mysql|mariadb --db-version VERSION [--db-port PORT]]

Create or update an environment definition in polka.yaml.
When name is omitted, Polka updates the current environment. If no current environment is selected, Polka uses default and marks it current after a successful config.
`

const installUsage = `Usage:
   polka install [name]

Install all configured tool versions for an environment.
When name is omitted, Polka installs the current environment. If no current environment is selected, Polka uses default and marks it current after a successful install.
Polka copies them from the global cache when available, otherwise downloads them into the cache first.
`

const dbUsage = `Usage:
  polka db [args...]
  polka db client [args...]
  polka db start
  polka db stop
  polka db status

Run or manage the active environment's configured database tool.
Polka dispatches to mysql or mariadb based on the current environment's database.engine setting in polka.yaml.

Subcommands:
  client   force client dispatch, even for reserved words such as status
  start    initialize the local data directory if needed and start the database server
  stop     stop the database server previously started by Polka
  status   show whether the managed database server is running

When no subcommand is provided, Polka forwards the arguments to the database client.
`

const serveUsage = `Usage:
	polka serve <docroot> [--server HOST:PORT]

Start the PHP local server for the active environment.
When --server is omitted, Polka uses the current environment's server.hostname and server.port from polka.yaml, defaulting to localhost:8000.
When the current environment defines a database, Polka starts that managed local database first.
`

const listUsage = `Usage:
  polka list

List the environments configured in polka.yaml.
`

const useUsage = `Usage:
  polka use <name>

Select one of the environments defined in polka.yaml.
`

const currentUsage = `Usage:
  polka current

Show the active environment from polka.yaml, if one has been selected.
`

const removeUsage = `Usage:
  polka remove <name>

Remove an environment definition from polka.yaml.
`
