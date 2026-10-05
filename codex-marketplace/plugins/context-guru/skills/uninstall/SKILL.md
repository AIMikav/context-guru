---
name: context-guru-uninstall
description: Restore Codex's previous default provider and stop the context-guru proxy. Use when asked to disable, remove, or uninstall it.
---

# Remove context-guru from Codex

Run `python3 ../../scripts/codex_plugin.py uninstall --dry-run` from this skill directory and show
what it found. Ask for confirmation, then rerun without `--dry-run` with
`sandbox_permissions="require_escalated"`, explaining that removal restores user configuration and
stops the owned local proxy. The script removes only the marked provider block, restores the
previous default provider unless the user changed it since installation, and only stops its
owned user service (with guarded recorded-process cleanup for older installations). The change
applies to new Codex sessions.
