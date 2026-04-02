# Security Policy

## Supported Versions

Only the latest commit on `main` is actively supported.

## Reporting a Vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Instead, report them privately via GitHub's [Security Advisories](../../security/advisories/new) feature (click **Report a vulnerability**).

Include:
- A description of the vulnerability and its potential impact
- Steps to reproduce or a proof-of-concept
- Affected file(s) and line numbers if known

You can expect an acknowledgement within **72 hours** and a patch or mitigation plan within **14 days** for confirmed issues.

## Scope

This project is a self-hosted Telegram shop bot. The following areas are in scope:

- Authentication and authorization (admin ID checks, webhook signature verification)
- Payment handling (Telegram Stars, CryptoBot webhooks)
- SQL injection or data leakage through user-supplied input
- Secrets exposure (tokens, keys) in logs or error messages

Out of scope: denial-of-service against a self-hosted instance, Telegram platform vulnerabilities.

## Security Best Practices for Deployers

- Store `BOT_TOKEN`, `CRYPTOBOT_TOKEN`, and `TELEGRAM_WEBHOOK_SECRET` in environment variables — never commit them.
- Set `ADMIN_IDS` to a minimal set of trusted Telegram user IDs.
- Run the bot behind a reverse proxy (nginx/Caddy) with TLS when using webhook mode.
- Keep the Docker image and Go dependencies up to date.
