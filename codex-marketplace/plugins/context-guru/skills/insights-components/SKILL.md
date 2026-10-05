---
name: context-guru-insights-components
description: Deterministically report measured context-guru component effectiveness.
---

# Analyze context-reduction components

Run `python3 ../../scripts/insights.py components`. Present the findings in their emitted order,
including every measurement label and exact fix. Do not perform arithmetic in prose.
If it reports `proxy_unavailable`, retry the same read-only command with
`sandbox_permissions="require_escalated"`, explaining that loopback access is needed. If the retry
cannot run, say the measurements are unverified rather than claiming the proxy is unavailable.
