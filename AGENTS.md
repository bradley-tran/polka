# Coding Agent Instructions

This document provides instructions for AI coding agents working on this repository.

## Project Overview

This is a Go CLI program for managing project-local PHP development environments. The project is organized around a small CLI layer, a backend orchestration layer, shared config types, and a tool plugin package that knows how to install and dispatch managed tools.

## Architecture
See [docs/architecture.md](docs/architecture.md) for a detailed overview of the project architecture, including package responsibilities and interactions.

## Coding Guidelines

1. **Use Go idioms**: Follow standard Go conventions for naming, error handling, and structuring code.
2. **Add comments**: Provide clear comments for all non-trivial functions, types, and complex logic. This includes tests. Retroactively add comments to existing code as needed.
3. **Don't ignore errors**: Always check and handle errors appropriately. Avoid using `_` to ignore errors unless it's intentional and justified.
4. **Write tests**: Ensure all new code is covered by tests, and existing tests pass before committing.

## Testing

- Tests are standard `*_test.go` files alongside source code
- Run `go test ./...` before committing changes
