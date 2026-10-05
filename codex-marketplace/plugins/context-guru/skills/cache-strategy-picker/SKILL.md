---
name: context-guru-cache-strategy-picker
description: Inspect or change the Codex prompt-cache keep-alive strategy.
---

# Choose a cache strategy

Run `python3 ../../scripts/codex_plugin.py configure --show`. Explain both choices and ask before
changing them: `30-min-ping` refreshes eligible OpenAI Responses caches shortly before their
30-minute lifetime and spends a small amount of the user's own quota; `none` sends no idle pings.
Then run `python3 ../../scripts/codex_plugin.py configure --cache-strategy NAME` and report whether
the proxy restarted successfully. Do not offer Anthropic's `5-min-ping` or `1-hour-head`; those are
different provider semantics.
