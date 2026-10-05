# context-guru for Codex

This is the Codex plugin bundle for context-guru. It routes ordinary Codex sessions through a
loopback proxy, exposes health and statistics skills, and can remove only the state it owns.

Register the GitHub marketplace and install the plugin:

```sh
codex plugin marketplace add rossoctl/context-guru
codex plugin add context-guru@context-guru
```

The first command clones and registers the GitHub marketplace. On the first run it can take a
minute or more, depending on GitHub/network speed and local disk performance, and may be quiet
while it downloads. Let it finish before running the second command.

Run marketplace registration from an ordinary shell. You can install the plugin afterward either
with the second command above or from `/plugins` inside Codex. Asking an existing Codex session to
run the shell commands is possible, but provides no advantage: Codex only loads newly installed
plugin skills in a new session, so you must restart Codex either way.

Start Codex and invoke the setup skill:

```text
$context-guru-setup
```

Setup asks before routing model traffic, preserves the currently selected provider as the proxy's
upstream, and updates `~/.codex/config.toml`. The change takes effect in the next session; after
that, start Codex normally with `codex`.

Codex uses OpenAI's Responses API. Content reduction defaults to `off`, so requests are forwarded
without trimming. Cache keep-alive is enabled by default: eligible OpenAI Responses sessions are
refreshed shortly before their 30-minute cache lifetime ends. You can opt into content reduction
separately after verifying the routed setup.

Use `$context-guru-preset-picker` to inspect or change the reduction level, and
`$context-guru-cache-strategy-picker` to inspect or change OpenAI cache keep-alive. Both preserve
the other setting and restart the owned proxy. `$context-guru-insights` produces a deterministic,
ranked report from measured proxy data; its cost arithmetic and exact fix commands come from code,
not model estimation.

For a narrower report, use `$context-guru-insights-capabilities`,
`$context-guru-insights-components`, or `$context-guru-insights-idle`.

During setup, the plugin downloads the latest `context-guru-proxy` release for the current platform,
verifies it against the release's SHA-256 checksums, and installs it into its private state directory.
An existing `context-guru-proxy` on `PATH` is reused instead. Codex requires proxy release v0.4.0
or newer.

Setup copies a standalone recovery command to
`~/.local/state/context-guru-codex/context-guru-reset`. Use it from an ordinary, unrouted shell if
the proxy is down and Codex cannot start. It restores the previous default provider, removes only
the marked provider block, and signals only the recorded process whose command still names the
recorded binary.

## Operations and troubleshooting

| Task | Command |
|---|---|
| Check routing and proxy health | `$context-guru-status` |
| Update the proxy binary | `$context-guru-update` |
| Remove routing from a healthy session | `$context-guru-uninstall` |
| Recover when routed sessions cannot run | `~/.local/state/context-guru-codex/context-guru-reset` |

Codex may ask permission when these skills need to access the local proxy, write configuration or
state under your home directory, download an update, or restart the proxy. A sandboxed command can
be blocked from `127.0.0.1` even when the proxy is healthy; status and insights therefore retry
their read-only health checks with permission instead of reporting a false outage.

The uninstall skill and recovery script restore the provider that was selected before setup. They
do not remove the Codex plugin registration. To remove that too, from an ordinary shell:

```sh
codex plugin remove context-guru@context-guru
codex plugin marketplace remove context-guru
```

To refresh the plugin code without removing it:

```sh
codex plugin marketplace upgrade context-guru
```

Restart Codex after installing or refreshing plugin code. Routing or proxy changes made by setup
take effect for model traffic in the next Codex process.
