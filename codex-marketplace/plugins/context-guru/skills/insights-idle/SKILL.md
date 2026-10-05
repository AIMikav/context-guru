---
name: context-guru-insights-idle
description: Deterministically report the measured value or cost of OpenAI cache keep-alive.
---

# Analyze idle cache behavior

Run `python3 ../../scripts/insights.py idle`. Preserve the measurement basis and exact fix. A
negative keep-alive result is measured spend and its emitted disable command must be shown exactly.
If it reports `proxy_unavailable`, retry the same read-only command with
`sandbox_permissions="require_escalated"`, explaining that loopback access is needed. If the retry
cannot run, say the measurements are unverified rather than claiming the proxy is unavailable.
