# context-guru for Codex

This is the Codex plugin bundle for context-guru. It routes ordinary Codex sessions through a
loopback proxy, exposes health and statistics skills, and can remove only the state it owns.

Register the GitHub marketplace and install the plugin:

```sh
codex plugin marketplace add rossoctl/context-guru
codex plugin add context-guru@context-guru
```

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

During setup, the plugin downloads the latest `context-guru-proxy` release for the current platform,
verifies it against the release's SHA-256 checksums, and installs it into its private state directory.
An existing `context-guru-proxy` on `PATH` is reused instead. Codex requires proxy release v0.4.0
or newer.

Setup copies a standalone recovery command to
`~/.local/state/context-guru-codex/context-guru-reset`. Use it from an ordinary, unrouted shell if
the proxy is down and Codex cannot start. It restores the previous default provider, removes only
the marked provider block, and signals only the recorded process whose command still names the
recorded binary.
