# Framework manifests

Framework integrations are embedded in `plugins/manifests/*.yaml`; assets live in `plugins/manifests/templates/*`. Adding a manifest registers the framework automatically on the next build. Project files select its `id` through `framework`. There is no runtime loading from disk.

The project schema version is `0.2` in `polka.yaml`. Embedded framework declarations do not have a separate version field. Legacy `runtime-env.database` and `post-composer.strategy` declarations are rejected.

## Example

```yaml
id: acme
defaults:
  docroot: public
  tools:
    php: "8.4"
    composer: "2.8"
    mariadb: "11.8"
  database:
    engine: mariadb
    port: 3306
database-engines: [mysql, mariadb, postgresql]
php-extensions: [mbstring, openssl]
detection:
  package-prefixes: [acme/]
  markers: [acme.lock]
  priority: 10
app-root:
  public-dir: public
  markers: [acme.lock, composer.json]
values:
  driver:
    by-engine: {mysql: mysql, mariadb: mysql, postgresql: pgsql}
runtime-env:
  requires-database: true
  values:
    DB_CONNECTION: {from: values.driver}
    DB_PASSWORD: {from: database.password}
post-composer:
  requires-database: true
  actions:
    - type: dotenv
      path: .env
      fallback: .env.example
      values:
        DB_CONNECTION: {from: runtime-env.DB_CONNECTION}
        DB_PASSWORD: {from: runtime-env.DB_PASSWORD}
      order: [DB_CONNECTION, DB_PASSWORD]
```

Existing metadata fields `defaults`, `php-extensions`, `database-engines`, and `opcache-config` retain their meanings. Defaults require a docroot; `defaults.framework`, if specified, must equal `id`. Framework database driver extensions still come from the configured database tool.

## Detection and application roots

`detection` accepts `package-prefixes`, `package-substrings`, `markers`, and integer `priority` (default `0`). Package names are compared case-insensitively with a trailing Composer version constraint removed. All prefix matches precede substring matches; package matches precede file markers. Within each category, higher priority wins, followed by ascending framework ID. Marker lists use any-match semantics and require regular files. A bare `composer.json` cannot be a detection marker. Filesystem errors other than missing files are reported.

`app-root.public-dir` and `app-root.markers` locate the framework within a project after Composer. Candidate order is the create-project target, the root inferred from the configured docroot, the working directory, and the project directory. The first candidate matching an app-root marker wins; otherwise the first candidate is used. Unlike detection, root hints may include `composer.json`.

## Structured values

Every value is a YAML scalar (string, number, boolean, or null) or one expression:

| Form | Meaning |
|---|---|
| `{from: database.host}` | Resolve a context field or named value |
| `{from: database.version, default: "11.8"}` | Substitute a default when the result is empty or null |
| `{by-engine: {mysql: value, mariadb: value, postgresql: value}}` | Select by the normalized database engine |
| `{format: {kind: string, value: binding}}` | Convert a scalar to text |
| `{format: {kind: host-port, host: binding, port: binding, omit-port: "3306"}}` | Compose a host and optional port, with IPv6 handling |
| `{format: {kind: version, value: binding, strip-prefix: mariadb-, min-parts: 3, prefix: mariadb-}}` | Normalize version components and affixes |

`database` fields are `engine`, `version`, `host`, `port`, `name`, `user`, and `password`, exposed as text. Host defaults to `127.0.0.1`; port defaults to `5432` for PostgreSQL and `3306` otherwise. An absent engine uses the environment engine, then `mysql`. Version uses the environment's explicit database version, then its selected tool version, then credential metadata. Database values require managed credentials. Normalization does not mutate caller data.

`values.<name>` references named values and `runtime-env.<key>` references declared runtime variables. Names retain their exact spelling, including dotted variable names. References and cycles are checked across all branches during loading. A `by-engine` expression must cover every engine in `database-engines`.

The URL formatter has structured components and omits empty query values:

```yaml
format:
  kind: url
  scheme: {from: values.scheme}
  user: {from: database.user}
  password: {from: database.password}
  host: {from: database.host}
  port: {from: database.port}
  path: {from: database.name}
  query:
    charset: {from: values.charset}
    serverVersion: {from: values.server-version}
```

`host`, `scheme`, and `path` bindings are required; other URL components are optional. User information, the database path component, and query values are escaped separately. Ports must be valid TCP ports. The built-in Symfony manifest shows per-engine URL and server-version declarations.

`runtime-env.requires-database` and `post-composer.requires-database` default to `false`. Set them to `true` for database-dependent integrations: their entire block then becomes a no-op without credentials. A database reference without credentials in an unguarded block returns an error. Runtime values are converted to strings.

## File actions

Every action has `type`, `path`, and optional `base` (`app-root`, the default, or `docroot`). Paths use forward slashes and must stay inside the chosen base; absolute, drive-qualified, backslash, traversal, and escaping symlink paths are rejected. Actions run in declaration order.

`fallback` names a file relative to the same base. `fallback-template-file` names an embedded asset. Existing targets take precedence over both. Only a missing file triggers fallback; read failures are returned. PHP editing actions require an embedded fallback if both target and project fallback are absent. A missing dotenv file can start empty; `php-include` can start with `<?php`.

| Type | Additional fields and behavior |
|---|---|
| `dotenv` | `values` maps assignment names to bindings. Optional `order` controls insertions; unlisted keys follow alphabetically. Preserves unrelated entries and escapes quoted values. |
| `php-array` | `updates` is a list of `{path: [Datasources, default], values: {...}, order: [...]}`. Paths locate containers in the returned PHP array. Missing literal branches are created; scalar leaves are updated. |
| `php-constants` | `values` maps constant names to bindings. Optional `order` controls insertion. `insert-before` names an existing constant used as an insertion anchor; otherwise missing constants are inserted before bootstrap includes or the PHP closing tag. |
| `write-file` | Requires `template-file`, `encoding` (`text` or `php-literal`), and bindings under `values`. Replaces `{{name}}` placeholders once. Generated files are managed and replaced on each run. |
| `php-include` | Requires `include`, a path relative to the target's directory, and `marker`, an identifier. Ensures a guarded `__DIR__` include between `// <polka:marker>` and `// </polka:marker>`. Existing managed blocks are refreshed and moved after application settings so installer-written credentials cannot override managed values. |

PHP arrays and constants preserve unrelated source, comments, and line endings. String values are PHP-quoted; booleans, numbers, and null retain their types. Literal short arrays and `array(...)` syntax are supported. Dynamic keys, computed containers, duplicate managed constants, heredocs, and other unsupported structures are refused without replacing the affected file. The editor never executes application PHP.

Targets are staged before replacement. Secret files use mode `0600`; include files use `0644`. Cross-file updates are ordered rather than transactional: an earlier successful action remains applied if a later action fails.

Unknown fields, old selectors, invalid references, cycles, missing template assets, unknown placeholders, conflicting expression/action fields, and duplicate or unknown `order` keys fail during loading. Keep regression tests beside the plugin code and run `go test ./...` after adding or changing a manifest.
