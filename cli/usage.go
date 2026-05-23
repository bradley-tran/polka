package cli

const rootUsage = `Polka manages isolated PHP virtual environments.

Usage:
  polka [--root PATH] <command> [options]

Commands:
	init                 create the local .polka directory and shims
	new <name>           create an environment with default or explicit versions
	config <name>        set php and composer versions for an environment
	install <name>       install all tools for an environment
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
	polka install api
	polka serve public
  polka list
  polka use api
  polka current

Flags:
	--root PATH          override the state directory (defaults to ./.polka)
`

const initUsage = `Usage:
  polka init

Create the local .polka directory, bootstrap polka.yaml, and install dispatch shims into .polka/bin.
`

const newUsage = `Usage:
	polka new <name> [--php VERSION] [--composer VERSION]

Create a new environment definition in polka.yaml.
When omitted, --php defaults to 8.4 and --composer defaults to 2.8.
`

const configUsage = `Usage:
	polka config <name> [--php VERSION] [--composer VERSION]

Create or update an environment definition in polka.yaml.
`

const installUsage = `Usage:
	 polka install <name>

Install all configured tool versions for an environment.
Polka copies them from the global cache when available, otherwise downloads them into the cache first.
`

const serveUsage = `Usage:
	polka serve <docroot> [--server HOST:PORT]

Start the PHP local server for the active environment.
When --server is omitted, Polka uses the current environment's server.hostname and server.port from polka.yaml, defaulting to localhost:8000.
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
