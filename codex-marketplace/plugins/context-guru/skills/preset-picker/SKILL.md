---
name: context-guru-preset-picker
description: Inspect or change how aggressively context-guru reduces Codex context.
---

# Choose a context-reduction preset

Run `python3 ../../scripts/codex_plugin.py configure --show`. Explain the choices without selecting
one for the user: `off` changes no content; `conservative` performs deterministic cleanup; `medium`
adds a paid relevance-model pass; `high` adds summarization; `xhigh` adds the deepest paid sweep.
Ask which one they want, then run `python3 ../../scripts/codex_plugin.py configure --preset NAME`.
Report the emitted settings and whether the proxy restarted successfully.
