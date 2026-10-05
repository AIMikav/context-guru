---
name: context-guru-status
description: Check Codex default routing, context-guru proxy health, and measured statistics. Use when asked about status, health, savings, or troubleshooting.
---

# Check context-guru for Codex

Run `python3 ../../scripts/codex_plugin.py status` from this skill directory. Report the config,
port, proxy health, and whether default routing is configured. A healthy proxy alone does not
prove that this session uses it. Summarize `stats_json` without inventing savings.

Codex command sandboxes may block requests to the local `127.0.0.1` health endpoint. If the
command reports `proxy_up=false`, retry the same read-only command with
`sandbox_permissions="require_escalated"`, explaining that loopback access is needed to verify
proxy health. Use the retried result for the report. If that check cannot run, say proxy health
is unverified; the sandboxed result alone does not establish that the proxy is down.
