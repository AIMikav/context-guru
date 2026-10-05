#!/usr/bin/env python3
"""Install and operate context-guru's default Codex routing."""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parent))
from config_route import install as install_default_route
from config_route import is_routed, restore as restore_default_route

MARKER = "# Managed by the context-guru Codex plugin."
CONFIG_MARKER = "# Managed by the context-guru Codex plugin."
RELEASE_REPO = "rossoctl/context-guru"
MINIMUM_CODEX_RELEASE = (0, 4, 0)


def codex_home():
    return Path(os.environ.get("CODEX_HOME", Path.home() / ".codex"))


def state_dir():
    base = Path(os.environ.get("XDG_STATE_HOME", Path.home() / ".local/state"))
    return base / "context-guru-codex"


def profile():
    return codex_home() / "context-guru.config.toml"


def main_config():
    return codex_home() / "config.toml"


def routing_state():
    return state_dir() / "routing.json"


def proxy_config():
    return state_dir() / "proxy.yaml"


def managed_binary():
    return state_dir() / "bin/context-guru-proxy"


def facts(**values):
    for key, value in values.items():
        if isinstance(value, (dict, list)):
            value = json.dumps(value, separators=(",", ":"))
        print(f"{key}={value}")


def healthy(port):
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{port}/healthz", timeout=1) as response:
            return response.status == 200
    except Exception:
        return False


def first_free_port():
    for port in range(8787, 8888):
        with socket.socket() as sock:
            try:
                sock.bind(("127.0.0.1", port))
                return port
            except OSError:
                pass
    raise RuntimeError("no free port in 8787..8887")


def read_record():
    try:
        return json.loads((state_dir() / "install.json").read_text())
    except (OSError, ValueError):
        return {}


def find_binary():
    found = shutil.which(os.environ.get("CONTEXT_GURU_BIN", "context-guru-proxy"))
    if found:
        return str(Path(found).resolve())
    if managed_binary().is_file() and os.access(managed_binary(), os.X_OK):
        return str(managed_binary().resolve())
    candidate = Path(__file__).resolve().parents[4] / "bin/context-guru-proxy"
    if candidate.is_file() and os.access(candidate, os.X_OK):
        return str(candidate)
    return None


def release_platform():
    systems = {"Darwin": "darwin", "Linux": "linux"}
    machines = {"x86_64": "amd64", "AMD64": "amd64",
                "arm64": "arm64", "aarch64": "arm64"}
    try:
        return systems[platform.system()], machines[platform.machine()]
    except KeyError as error:
        raise RuntimeError(f"unsupported release platform: {platform.system()}/{platform.machine()}") from error


def latest_release_tag():
    request = urllib.request.Request(
        f"https://github.com/{RELEASE_REPO}/releases/latest", method="HEAD")
    with urllib.request.urlopen(request, timeout=15) as response:
        tag = response.geturl().rstrip("/").rsplit("/", 1)[-1]
    if not tag or tag == "latest":
        raise RuntimeError("could not resolve the latest context-guru release")
    return tag


def fetch(url):
    with urllib.request.urlopen(url, timeout=60) as response:
        return response.read()


def release_version(tag):
    value = tag.removeprefix("v").split("-", 1)[0]
    try:
        parts = tuple(int(part) for part in value.split("."))
    except ValueError as error:
        raise RuntimeError(f"unrecognized context-guru release tag: {tag}") from error
    if len(parts) != 3:
        raise RuntimeError(f"unrecognized context-guru release tag: {tag}")
    return parts


def install_release_binary(version=None):
    version = version or os.environ.get("CONTEXT_GURU_VERSION") or latest_release_tag()
    if release_version(version) < MINIMUM_CODEX_RELEASE:
        raise RuntimeError(
            f"release {version} predates Codex Responses support; v0.4.0 or newer is required")
    os_name, arch = release_platform()
    number = version.removeprefix("v")
    archive_name = f"context-guru_{number}_{os_name}_{arch}.tar.gz"
    base = f"https://github.com/{RELEASE_REPO}/releases/download/{version}"
    archive = fetch(f"{base}/{archive_name}")
    checksums = fetch(f"{base}/checksums.txt").decode("utf-8")
    expected = None
    for line in checksums.splitlines():
        fields = line.split()
        if len(fields) >= 2 and fields[-1].lstrip("*") == archive_name:
            expected = fields[0].lower()
            break
    if not expected:
        raise RuntimeError(f"release checksum does not list {archive_name}")
    actual = hashlib.sha256(archive).hexdigest()
    if actual != expected:
        raise RuntimeError(f"release checksum mismatch for {archive_name}")

    destination = managed_binary()
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as bundle:
        matches = [member for member in bundle.getmembers()
                   if member.isfile() and Path(member.name).name == "context-guru-proxy"]
        if len(matches) != 1:
            raise RuntimeError("release archive does not contain exactly one context-guru-proxy")
        source = bundle.extractfile(matches[0])
        if source is None:
            raise RuntimeError("could not read context-guru-proxy from release archive")
        fd, temporary_name = tempfile.mkstemp(prefix="context-guru-proxy.",
                                               dir=destination.parent)
        temporary = Path(temporary_name)
        try:
            with os.fdopen(fd, "wb") as output:
                shutil.copyfileobj(source, output)
            temporary.chmod(0o755)
            os.replace(temporary, destination)
        finally:
            temporary.unlink(missing_ok=True)
    return str(destination.resolve()), version


def install_escape_hatch():
    source = Path(__file__).with_name("reset.sh")
    target = state_dir() / "context-guru-reset"
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, target)
    target.chmod(0o700)
    helper = state_dir() / "config_route.py"
    shutil.copyfile(Path(__file__).with_name("config_route.py"), helper)
    helper.chmod(0o700)
    return target


def write_proxy_config():
    path = proxy_config()
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.exists() and not path.read_text().startswith(CONFIG_MARKER):
        raise RuntimeError(f"refusing to overwrite unmanaged proxy config: {path}")
    temporary = path.with_suffix(".tmp")
    temporary.write_text(
        f"{CONFIG_MARKER}\n"
        "preset: off\n"
        "cache:\n"
        "  keepalive: true\n"
    )
    temporary.chmod(0o600)
    os.replace(temporary, path)
    return path


def start_proxy(executable, port, upstream=None):
    root = state_dir()
    root.mkdir(parents=True, exist_ok=True)
    log = open(root / "proxy.log", "ab", buffering=0)
    process = subprocess.Popen(
        proxy_command(executable, port, upstream),
        stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True,
    )
    for _ in range(40):
        if healthy(port):
            return process
        if process.poll() is not None:
            return None
        time.sleep(0.1)
    process.terminate()
    return None


def proxy_command(executable, port, upstream=None):
    root = state_dir()
    command = [executable, "--listen", f"127.0.0.1:{port}", "--config", str(proxy_config()),
               "--dashboard", "--dashboard-db", str(root / "dashboard.db")]
    if upstream:
        command.extend(["--openai-upstream", upstream.rstrip("/")])
    return command


def base_provider():
    path = codex_home() / "config.toml"
    try:
        lines = path.read_text().splitlines()
    except OSError:
        return {}
    name = None
    for line in lines:
        key, separator, value = line.partition("=")
        if separator and key.strip() == "model_provider":
            try:
                name = json.loads(value.strip())
            except (ValueError, TypeError):
                return {}
            break
    if not isinstance(name, str):
        return {}
    section = f'[model_providers.{name}]'
    provider = {}
    active = False
    for line in lines:
        stripped = line.strip()
        if stripped.startswith("["):
            active = stripped == section
            continue
        if not active:
            continue
        key, separator, value = line.partition("=")
        key = key.strip()
        if not separator or key not in {"base_url", "experimental_bearer_token",
                                        "requires_openai_auth"}:
            continue
        value = value.strip()
        if value in {"true", "false"}:
            provider[key] = value == "true"
        else:
            try:
                provider[key] = json.loads(value)
            except (ValueError, TypeError):
                pass
    return provider


def setup(args):
    if args.plan:
        facts(result="planned", scope="user", config=main_config(),
              reset=state_dir() / "context-guru-reset",
              consent_question="Route every new Codex session on this machine through a local "
                               "context-guru proxy, with cache keep-alive using your own quota?")
        return 0
    if not args.i_consent_to_traffic_interception:
        facts(result="refused", reason="consent_required")
        return 2
    try:
        write_proxy_config()
    except Exception as error:
        facts(result="config_conflict", config=proxy_config(), detail=error)
        return 2
    executable = find_binary()
    if not executable:
        try:
            executable, version = install_release_binary()
            facts(binary="installed", binary_version=version, binary_path=executable)
        except Exception as error:
            facts(result="binary_install_failed", detail=error)
            return 2
    existing = read_record()
    provider = existing.get("provider") if is_routed(main_config()) else None
    if not isinstance(provider, dict):
        provider = base_provider()
    port = int(existing.get("port", 0))
    if port and healthy(port):
        try:
            install_default_route(main_config(), routing_state(), port, provider)
            reset = install_escape_hatch()
        except Exception as error:
            facts(result="setup_failed", detail=error)
            return 1
        existing.update(config=str(main_config()), provider=provider,
                        upstream=provider.get("base_url"))
        (state_dir() / "install.json").write_text(json.dumps(existing, indent=2) + "\n")
        old_profile = profile()
        if old_profile.exists() and old_profile.read_text().startswith(MARKER):
            old_profile.unlink()
        facts(result="already_running", port=port, config=main_config(), reset=reset,
              launch="codex")
        return 0
    port = first_free_port()
    root = state_dir()
    root.mkdir(parents=True, exist_ok=True)
    upstream = provider.get("base_url")
    process = start_proxy(executable, port, upstream)
    if process is None:
        facts(result="start_failed", log=root / "proxy.log")
        return 1
    try:
        install_default_route(main_config(), routing_state(), port, provider)
        reset = install_escape_hatch()
    except Exception as error:
        process.terminate()
        facts(result="setup_failed", detail=error)
        return 1
    old_profile = profile()
    if old_profile.exists() and old_profile.read_text().startswith(MARKER):
        old_profile.unlink()
    record = {"port": port, "pid": process.pid, "binary": executable,
              "config": str(main_config()), "upstream": upstream, "provider": provider}
    (root / "install.json").write_text(json.dumps(record, indent=2) + "\n")
    facts(result="installed", port=port, config=main_config(), reset=reset, launch="codex")
    return 0


def ensure(_args):
    record = read_record()
    port = int(record.get("port", 0))
    executable = record.get("binary")
    if not port or not executable or not is_routed(main_config()):
        return 0
    if healthy(port):
        return 0
    process = start_proxy(executable, port, record.get("upstream"))
    if process is None:
        facts(result="start_failed", log=state_dir() / "proxy.log")
        return 0
    record["pid"] = process.pid
    (state_dir() / "install.json").write_text(json.dumps(record, indent=2) + "\n")
    facts(result="restarted", port=port)
    return 0


def serve(_args):
    """Keep the proxy attached to Codex's asynchronous SessionStart hook."""
    record = read_record()
    port = int(record.get("port", 0))
    executable = record.get("binary")
    if not port or not executable or not is_routed(main_config()) or healthy(port):
        return 0
    record["pid"] = os.getpid()
    (state_dir() / "install.json").write_text(json.dumps(record, indent=2) + "\n")
    log_fd = os.open(state_dir() / "proxy.log", os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
    os.dup2(log_fd, 1)
    os.dup2(log_fd, 2)
    if log_fd > 2:
        os.close(log_fd)
    os.execv(executable, proxy_command(executable, port, record.get("upstream")))


def status(_args):
    record = read_record()
    port = int(record.get("port", 0))
    up = bool(port and healthy(port))
    configured = is_routed(main_config())
    values = {"result": "ok" if up and configured else "not_ready", "config": main_config(),
              "default_routing_configured": str(configured).lower(), "port": port or "(none)",
              "proxy_up": str(up).lower(), "session_routed": "unknown"}
    if up:
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/stats", timeout=2) as response:
                values["stats_json"] = json.load(response)
        except Exception as error:
            values["stats_error"] = str(error)
    facts(**values)
    return 0 if up and configured else 1


def stop_owned(record):
    pid = record.get("pid")
    if not isinstance(pid, int):
        return "gone"
    try:
        command = subprocess.check_output(
            ["ps", "-p", str(pid), "-o", "command="], text=True).strip()
    except subprocess.CalledProcessError:
        return "gone"
    except OSError:
        return "not_owned"
    expected = record.get("binary", "")
    if not expected or not command.startswith(expected + " "):
        return "not_owned"
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        return "gone"
    return "stopped"


def update(args):
    try:
        latest = latest_release_tag()
    except Exception as error:
        facts(result="error", reason="release_check_failed", detail=error)
        return 1
    executable = find_binary()
    installed = "unknown"
    if executable:
        completed = subprocess.run([executable, "--version"], text=True, capture_output=True)
        if completed.returncode == 0:
            fields = completed.stdout.split()
            if len(fields) >= 2:
                installed = fields[1]
    available = installed.removeprefix("v") != latest.removeprefix("v")
    if args.check:
        facts(result="checked", installed_version=installed, latest_version=latest,
              update_available=str(available).lower())
        return 0
    try:
        executable, version = install_release_binary(latest)
    except Exception as error:
        facts(result="error", reason="binary_install_failed", detail=error)
        return 1
    record = read_record()
    port = int(record.get("port", 0))
    stopped = stop_owned(record)
    if stopped == "not_owned" or not port or not executable:
        facts(restart="skipped", reason=stopped if stopped == "not_owned" else "incomplete_record")
        return 1
    process = start_proxy(executable, port, record.get("upstream"))
    if process is None:
        facts(restart="failed", port=port)
        return 1
    record.update(pid=process.pid, binary=executable)
    (state_dir() / "install.json").write_text(json.dumps(record, indent=2) + "\n")
    facts(result="updated", version=version, restart="completed", port=port)
    return 0


def uninstall(args):
    record = read_record()
    routed = is_routed(main_config())
    facts(result="planned" if args.dry_run else "removing", config=main_config(),
          default_routing_configured=str(routed).lower(), pid=record.get("pid", "(none)"))
    if args.dry_run:
        return 0
    process_result = stop_owned(record)
    if process_result == "not_owned":
        facts(process="not_owned")
    restore_result = restore_default_route(routing_state())
    facts(config_restore=restore_result)
    path = profile()
    if path.exists() and path.read_text().startswith(MARKER):
        path.unlink()
    config = proxy_config()
    if config.exists() and config.read_text().startswith(CONFIG_MARKER):
        config.unlink()
    install = state_dir() / "install.json"
    if install.exists() and process_result != "not_owned":
        install.unlink()
    facts(result="removed")
    return 0


def main():
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    install = commands.add_parser("setup")
    install.add_argument("--plan", action="store_true")
    install.add_argument("--i-consent-to-traffic-interception", action="store_true")
    commands.add_parser("status")
    commands.add_parser("ensure")
    commands.add_parser("serve")
    upgrade = commands.add_parser("update")
    mode = upgrade.add_mutually_exclusive_group(required=True)
    mode.add_argument("--check", action="store_true")
    mode.add_argument("--install", action="store_true")
    remove = commands.add_parser("uninstall")
    remove.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    return {"setup": setup, "status": status, "ensure": ensure, "serve": serve,
            "update": update, "uninstall": uninstall}[args.command](args)


if __name__ == "__main__":
    sys.exit(main())
