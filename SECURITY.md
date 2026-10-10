# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in Memory, please report it responsibly:

1. **Do NOT open a public GitHub issue.**
2. Use [GitHub Security Advisories](https://github.com/JayWanX/memory/security/advisories/new) to report privately.
3. Include steps to reproduce, affected versions, and potential impact.

We will acknowledge receipt within 48 hours and aim to release a fix within 7 days for critical issues.

## Scope

Memory runs locally and stores data in `~/.memory/`. Key security considerations:

- **SQLite database** — contains all stored insights; protected by filesystem permissions (`0644`).
- **Hook scripts** — shell scripts executed by the LLM CLI at lifecycle events; written with `0755` permissions.
- **Embedding provider connection** — optional requests send insight or query text to the configured Ollama or OpenAI-compatible server. The default local Ollama endpoint does not use TLS. If `MEMORY_EMBED_ENDPOINT` points outside a trusted local network, use HTTPS to protect content and any `MEMORY_EMBED_API_KEY` bearer token in transit.

## Supported Versions

| Version | Supported |
|---|---|
| Latest release | Yes |
| Older releases | Best effort |
