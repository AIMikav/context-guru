---
name: context-guru-update
description: Check for and install a newer context-guru proxy binary. Use when the user asks to update, upgrade, or check the context-guru version.
---

# Update the proxy binary

Codex does not run this check automatically. Tell the user to invoke `$context-guru-update`
periodically if they want to stay current; do not claim there is a background update check.

Run `python3 ../../scripts/codex_plugin.py update --check` from this skill directory first. This is
read-only and reports the installed and latest versions. If the check fails, say the version is
unknown rather than claiming it is current. If it fails because the command sandbox blocks the
release-network request, retry the same read-only check with
`sandbox_permissions="require_escalated"` and explain why.

Only after the user explicitly asks to install the update, run
`python3 ../../scripts/codex_plugin.py update --install`. This downloads and checksum-verifies the
release using the shared installer, then safely restarts only context-guru's owned user service.
Run installation with `sandbox_permissions="require_escalated"`, explaining that it downloads and
writes the binary and restarts the local proxy. Report the installer's result verbatim when it
refuses or fails.
