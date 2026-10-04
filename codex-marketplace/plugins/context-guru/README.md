# context-guru for Codex

This is the Codex plugin bundle for context-guru. It installs a dedicated Codex profile, starts a
loopback proxy, exposes health and statistics skills, and can remove only the state it owns.

Clone, build the proxy, then register and install the marketplace:

```sh
git clone --depth 1 https://github.com/rossoctl/context-guru.git
make -C context-guru build
codex plugin marketplace add ./context-guru/codex-marketplace
codex plugin add context-guru@context-guru
```

Start Codex and ask it to **set up context-guru**. The setup skill deliberately does not edit
`~/.codex/config.toml`. After setup, start a routed session with:

```sh
codex -p context-guru
```

Codex uses OpenAI's Responses API. Content reduction defaults to `off`, so requests are forwarded
without trimming. Cache keep-alive is enabled by default: eligible OpenAI Responses sessions are
refreshed shortly before their 30-minute cache lifetime ends. You can opt into content reduction
separately after verifying the routed setup.

The plugin requires `context-guru-proxy` on `PATH`, or a binary built at
`bin/context-guru-proxy` in this checkout.

Setup copies a standalone recovery command to
`~/.local/state/context-guru-codex/context-guru-reset`. Use it from an ordinary, unrouted shell if
the proxy is down and the routed profile cannot start Codex. It backs up and removes only the
marked profile, and signals only the recorded process whose command still names the recorded
binary.
