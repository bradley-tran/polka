# Polka

Polka is a CLI tool for PHP virtual environment management. It uses `polka.yaml` as the source of truth for environment selection, and the shims in `.polka/bin` dispatch to the locally installed `php` and `composer` versions selected for the active environment.

## What is included

- A lean Go module with a runnable CLI entrypoint.
- A project-local layout built around `polka.yaml` and `.polka/`.
- Starter commands for `init`, `new`, `install`, `serve`, `config`, `list`, `use`, `current`, and `remove`.
- Real dispatch shims in `.polka/bin` for `php` and `composer`.
- A global tool cache used to avoid re-downloading versions across projects.
- A small test covering the basic environment lifecycle.

## Project layout

```text
.
|-- .polka/     # local environments and shims
|-- cli/        # argument parsing and command handlers
|-- backend/    # environment metadata and filesystem state
|-- main.go     # CLI entrypoint
|-- polka.yaml  # project config and current environment
`-- README.md
```

## Quick start

```bash
go test ./...
go run . init
go run . new blog
go run . install blog
go run . serve public
go run . list
go run . use blog
go run . current
./.polka/bin/php -v
```

`polka init` creates a local `.polka/` directory, copies the current `polka` executable into `.polka/bin`, syncs the active environment's dispatch shims in `.polka/bin`, and writes `polka.yaml` if it does not exist.

Use `polka new <name> [--php VERSION] [--composer VERSION]` to create a new environment definition. When the flags are omitted, Polka currently defaults to `php=8.4` and `composer=2.8`.

Use `polka install [name]` to install every configured tool version for an environment. When `name` is omitted, Polka installs the current environment and prints which one it selected. If no current environment is selected, Polka uses `default` and marks it current after a successful install. Polka first checks the global cache, then downloads any missing versions into that cache, and finally copies the cached payloads into the project-local `.polka/envs` layout.

The shims in `.polka/bin` mirror the active environment's configured tools. If the current environment does not define `composer`, Polka removes the local `composer` shim instead of leaving a dispatcher that would fail at runtime.

Use `polka serve <docroot> [--server HOST:PORT]` to start the active environment's web server. When the current environment defines `nginx`, Polka starts `php-cgi` on an internal loopback port and runs nginx in the foreground with a generated FastCGI config; otherwise it falls back to PHP's built-in web server. Polka reads `server.hostname` and `server.port` from the current environment in `polka.yaml`, and falls back to `localhost:8000` when that config is absent. Automatic nginx downloads are currently implemented on Windows amd64.

When an environment defines `php-extensions`, `polka install` also writes a generated `php.ini` next to the installed PHP executable so those extensions are explicitly enabled or disabled for that environment. If `composer` is configured for that environment, `openssl` and `zip` are enabled by default unless `php-extensions` explicitly sets either one to `false`.

Use `polka config [name] --php <version> --composer <version>` to update an existing environment definition. When `name` is omitted, Polka updates the current environment, or `default` when no environment is selected yet. The config file stores version labels, not machine-specific executable paths:

```yaml
version: 1
root: .polka
current: blog
environments:
  blog:
    php: 8.4
    composer: 2.8
    nginx: 1.30
    server:
      hostname: localhost
      port: 8080
    php-extensions:
      openssl: true
      xdebug: false
  legacy:
    php: 8.2
    composer: 2.6
```

Polka resolves those versions against the local install layout under `.polka/envs`:

```text
.polka/
|-- envs/
|   |-- composer/
|   |   `-- 2.8/
|   |       `-- bin/composer[.cmd|.bat|.exe|.phar]
|   `-- php/
|       `-- 8.4/
|           `-- bin/php[.exe|.cmd|.bat]
`-- bin/
```

For example, this sequence records a version label in `polka.yaml`, installs that environment from cache or download, and then dispatches through the generated shim:

```bash
go run . new blog
go run . install blog
go run . use blog
./.polka/bin/php -v
```

If you want a non-default version at creation time, pass it explicitly:

```bash
go run . new legacy --php 8.2 --composer 2.6
```

That keeps `polka.yaml` portable across machines while letting each machine reuse a shared cache and install project-local copies from it.

During development you can override the state directory while keeping the config file next to it:

```bash
go run . --root ./.polka-dev init
```

## Next implementation steps

1. Add cache metadata and eviction so old downloaded versions can be pruned safely.
2. Allow empty enviroment name (use default) for applicable commands
3. Generate activation scripts or shell hooks.
