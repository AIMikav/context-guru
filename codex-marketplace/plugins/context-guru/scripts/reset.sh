#!/bin/sh
# Standalone recovery path when Codex's routed local proxy cannot answer.
set -eu

DRY=0
YES=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY=1 ;;
    --yes|-y) YES=1 ;;
    -h|--help) echo "usage: context-guru-reset [--dry-run] [--yes]"; exit 0 ;;
    *) echo "context-guru-reset: unknown argument: $arg" >&2; exit 2 ;;
  esac
done

CODEX_DIR=${CODEX_HOME:-"$HOME/.codex"}
STATE_BASE=${XDG_STATE_HOME:-"$HOME/.local/state"}
STATE_DIR="$STATE_BASE/context-guru-codex"
PROFILE="$CODEX_DIR/context-guru.config.toml"
MAIN_CONFIG="$CODEX_DIR/config.toml"
PROXY_CONFIG="$STATE_DIR/proxy.yaml"
ROUTING_STATE="$STATE_DIR/routing.json"
ROUTING_HELPER="$STATE_DIR/config_route.py"
RECORD="$STATE_DIR/install.json"
MARKER='# Managed by the context-guru Codex plugin.'

owned=0
if [ -f "$PROFILE" ] && [ "$(sed -n '1p' "$PROFILE")" = "$MARKER" ]; then owned=1; fi
config_owned=0
if [ -f "$PROXY_CONFIG" ] && [ "$(sed -n '1p' "$PROXY_CONFIG")" = "$MARKER" ]; then config_owned=1; fi
echo "profile=$PROFILE"
echo "profile_owned=$owned"
echo "config=$MAIN_CONFIG"
echo "proxy_config=$PROXY_CONFIG"
echo "proxy_config_owned=$config_owned"
[ -f "$RECORD" ] && echo "record=$RECORD" || echo "record=(none)"

if [ "$DRY" = 1 ]; then echo "result=planned"; exit 0; fi
if [ "$YES" != 1 ]; then
  printf 'Restore Codex default routing and stop its recorded context-guru proxy? [y/N] '
  read answer
  case "$answer" in y|Y|yes|YES) ;; *) echo "result=cancelled"; exit 0 ;; esac
fi

if [ "$owned" = 1 ]; then
  mkdir -p "$STATE_DIR/recovery"
  stamp=$(date +%Y%m%d-%H%M%S)
  cp "$PROFILE" "$STATE_DIR/recovery/context-guru.config.toml.$stamp"
  chmod 600 "$STATE_DIR/recovery/context-guru.config.toml.$stamp"
  rm "$PROFILE"
  echo "profile_removed=true"
fi
if [ -f "$ROUTING_STATE" ] && [ -f "$ROUTING_HELPER" ]; then
  mkdir -p "$STATE_DIR/recovery"
  stamp=${stamp:-$(date +%Y%m%d-%H%M%S)}
  if [ -f "$MAIN_CONFIG" ]; then
    cp "$MAIN_CONFIG" "$STATE_DIR/recovery/config.toml.pre-reset-$stamp"
    chmod 600 "$STATE_DIR/recovery/config.toml.pre-reset-$stamp"
  fi
  python3 "$ROUTING_HELPER" restore "$ROUTING_STATE"
else
  echo "config_restore=no_record"
fi
if [ "$config_owned" = 1 ]; then
  rm "$PROXY_CONFIG"
  echo "proxy_config_removed=true"
fi

# Read only integer PID and path-shaped binary fields; never eval record content.
process_state=gone
if [ -f "$RECORD" ] && command -v python3 >/dev/null 2>&1; then
  pid=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d.get("pid", ""))' "$RECORD" 2>/dev/null || true)
  bin=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d.get("binary", ""))' "$RECORD" 2>/dev/null || true)
  case "$pid" in ''|*[!0-9]*) pid= ;; esac
  if [ -n "$pid" ] && [ -n "$bin" ]; then
    command=$(ps -p "$pid" -o command= 2>/dev/null || true)
    case "$command" in
      "$bin "*) kill "$pid" 2>/dev/null || true; process_state=stopped; echo "proxy_stopped=true" ;;
      *) process_state=not_owned; echo "proxy_stopped=false"; echo "reason=process_not_owned" ;;
    esac
  fi
fi
[ "$process_state" = not_owned ] || rm -f "$RECORD"
echo "result=removed"
echo "recovery=$STATE_DIR/recovery"
