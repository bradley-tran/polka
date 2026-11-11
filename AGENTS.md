# Coding Agent Instructions

This document provides instructions for AI coding agents working on this repository.

## Project Overview

This is a D (`ldc2` / `dub`) CLI program for managing project-local PHP development environments. The project is organized under `source/` around a small CLI layer (`polka.cli`), a backend orchestration layer (`polka.backend`), shared config types (`polka.config`), runtime services (`polka.service`), framework/preset plugins (`polka.plugins`), managed tool plugins (`polka.tools`), and shared low-level utilities (`polka.support`), embedding compile-time YAML manifests from `tools/manifests/` and `plugins/manifests/`.

## Architecture
See [docs/architecture.md](docs/architecture.md) for a detailed overview of the project architecture, including package responsibilities and interactions.

## Coding Guidelines

1. **Use D idioms**: Follow standard D (`ldc2` / Phobos) conventions for naming, error handling, RAII cleanup (`scope (exit)`), and structuring modules.
2. **Add comments**: Provide clear comments for all non-trivial functions, types, and complex logic. This includes tests. Retroactively add comments to existing code as needed.
3. **Don't ignore errors**: Always check and handle errors/exceptions appropriately. Avoid swallowing exceptions or ignoring return codes unless intentional and justified.
4. **Write tests**: Ensure all new code is covered by `unittest` blocks, and existing tests pass before committing.

## Testing

- Tests are inline `unittest` blocks (and integration test modules such as `source/polka/cli/cli_e2e_test.d`) built via `dub`
- Run `dub test` (or `make test`) before committing changes
