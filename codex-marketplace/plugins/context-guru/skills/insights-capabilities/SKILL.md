---
name: context-guru-insights-capabilities
description: Deterministically report unused tools and capabilities measured by context-guru.
---

# Analyze carried capabilities

Run `python3 ../../scripts/insights.py capabilities`. Present the findings in their emitted order,
including their basis and exact fix. Never calculate a cost or infer that an unpriced item is free.
If it reports `proxy_unavailable`, retry the same read-only command with
`sandbox_permissions="require_escalated"`, explaining that loopback access is needed. If the retry
cannot run, say the measurements are unverified rather than claiming the proxy is unavailable.
