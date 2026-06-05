# Contributing to Flock

Thanks for your interest in contributing.

The primary contributor guide lives in [ARCHITECTURE.md → Getting started as a contributor](ARCHITECTURE.md#getting-started-as-a-contributor). Read that first.

## Quick links

- **First-time contributors**: [ARCHITECTURE.md → Your first 30 minutes](ARCHITECTURE.md#getting-started-as-a-contributor)
- **Open tasks**: [TASKS.md](TASKS.md)
- **Design rationale**: [ARCHITECTURE.md](ARCHITECTURE.md)
- **Code of Conduct**: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
- **Security disclosures**: [SECURITY.md](SECURITY.md)

## PR checklist

1. Open an issue first if the change is non-trivial
2. Branch from `main`: `feat/<short-name>` or `fix/<short-name>`
3. One change per PR
4. Add or update tests and docs in the same PR
5. `make check` must pass locally
6. PR title references the task ID if applicable (e.g. `M1-T07: add API key bootstrap`)
7. Two maintainer reviews required to merge

## Reporting bugs

File an issue with:
- Flock version (`flock version`)
- OS and architecture
- Output of `flock doctor`
- Minimal reproduction

## Asking questions

Use GitHub Discussions for design questions, RFCs, and "is this a bug?". Use GitHub Issues only for confirmed bugs and concrete feature requests.
