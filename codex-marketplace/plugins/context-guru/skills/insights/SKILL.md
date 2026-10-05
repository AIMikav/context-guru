---
name: context-guru-insights
description: Analyze measured context, token, prompt-cache, idle-time, and tool costs. Use when asked what wastes tokens or money, what to optimize, or for context-guru recommendations.
---

# Analyze Codex context cost

Run `python3 ../../scripts/insights.py`. Report its ranked findings in order and preserve every
measurement label and fix command exactly. The script—not the model—does the arithmetic and sorting.
Stop on `not_installed` or `proxy_unavailable`. On a subscription plan, describe dollar values as
estimated usage-limit value rather than a lower bill.
