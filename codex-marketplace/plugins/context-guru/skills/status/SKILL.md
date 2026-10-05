---
name: context-guru-status
description: Check Codex default routing, context-guru proxy health, and measured statistics. Use when asked about status, health, savings, or troubleshooting.
---

# Check context-guru for Codex

Run `python3 ../../scripts/codex_plugin.py status` from this skill directory. Report the config,
port, proxy health, and whether default routing is configured. A healthy proxy alone does not
prove that this session uses it. Summarize `stats_json` without inventing savings.
