# Security Policy

## Supported versions

Flock is pre-1.0. Security fixes ship on `main` and in the most recent minor release. Older minor releases are not patched.

| Version | Supported |
|---------|-----------|
| `main`  | ✅ |
| Latest tagged minor | ✅ |
| Older  | ❌ |

## Reporting a vulnerability

**Do not file a public issue for security bugs.**

Email: `security@flock.dev` *(placeholder until a real address is set up — until then, please open a private GitHub Security Advisory at https://github.com/hadihonarvar/flock/security/advisories/new)*

Include:
- A description of the issue and its impact
- Steps to reproduce
- Flock version, OS, and architecture
- Whether you intend to publish details and when

## Disclosure timeline

- **Day 0**: report received; acknowledgement within 48 hours
- **Day 7**: triage complete; severity assigned (CVSS 3.1)
- **Day 30**: fix in `main`, backports if applicable
- **Day 90**: public advisory and CVE if applicable

We coordinate disclosure timing with reporters when possible.

## Scope

In scope:

- The `flock` binary and all code in this repository
- Official Docker images we publish
- Pre-built install scripts hosted at `get.flock.dev`

Out of scope:

- Upstream inference engines (vLLM, Ollama, MLX-LM, llama.cpp) — report to those projects
- Self-hosted deployments of Flock that have been modified
- Hypothetical issues without a reproduction

## Hall of fame

Credit will be given here for responsibly disclosed reports, with the reporter's permission.
