import importlib.util
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest import mock

MODULE_PATH = Path(__file__).with_name("codex_plugin.py")
SPEC = importlib.util.spec_from_file_location("codex_plugin", MODULE_PATH)
PLUGIN = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PLUGIN)


class CodexPluginTest(unittest.TestCase):
    def test_session_start_hook_is_async(self):
        hooks_path = MODULE_PATH.parent.parent / "hooks.json"
        hooks = json.loads(hooks_path.read_text())
        handler = hooks["hooks"]["SessionStart"][0]["hooks"][0]
        self.assertEqual(handler["command"], "python3 ./scripts/codex_plugin.py serve")
        self.assertIs(handler["async"], True)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        self.env = mock.patch.dict(os.environ, {
            "HOME": str(root / "home"),
            "CODEX_HOME": str(root / "codex"),
            "XDG_STATE_HOME": str(root / "state"),
        }, clear=False)
        self.env.start()

    def tearDown(self):
        self.env.stop()
        self.temp.cleanup()

    def test_profile_is_isolated_and_uses_responses_wire_api(self):
        PLUGIN.write_profile(8791)
        text = PLUGIN.profile().read_text()
        self.assertTrue(text.startswith(PLUGIN.MARKER))
        self.assertIn('base_url = "http://127.0.0.1:8791/openai/v1"', text)
        self.assertIn('wire_api = "responses"', text)
        self.assertFalse((PLUGIN.codex_home() / "config.toml").exists())

    def test_profile_and_proxy_preserve_custom_provider_route(self):
        provider = {
            "base_url": "https://gateway.example.test/",
            "requires_openai_auth": False,
            "experimental_bearer_token": "test-token",
        }
        PLUGIN.write_profile(8791, provider)
        text = PLUGIN.profile().read_text()
        self.assertIn('experimental_bearer_token = "test-token"', text)
        self.assertIn("requires_openai_auth = false", text)
        self.assertEqual(PLUGIN.profile().stat().st_mode & 0o777, 0o600)
        command = PLUGIN.proxy_command("/proxy", 8791, provider["base_url"])
        self.assertEqual(command[3:5], ["--config", str(PLUGIN.proxy_config())])
        self.assertEqual(command[-2:], ["--openai-upstream", "https://gateway.example.test"])

    def test_proxy_config_uses_off_preset_and_cache_keepalive(self):
        path = PLUGIN.write_proxy_config()
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(path.read_text(),
                         f"{PLUGIN.CONFIG_MARKER}\npreset: off\ncache:\n  keepalive: true\n")

    def test_refuses_unmanaged_proxy_config(self):
        PLUGIN.proxy_config().parent.mkdir(parents=True)
        PLUGIN.proxy_config().write_text("preset: mine\n")
        with self.assertRaisesRegex(RuntimeError, "unmanaged proxy config"):
            PLUGIN.write_proxy_config()

    def test_reads_selected_base_provider_without_external_toml_dependency(self):
        PLUGIN.codex_home().mkdir(parents=True)
        (PLUGIN.codex_home() / "config.toml").write_text('''
model_provider = "gateway"
[model_providers.other]
base_url = "https://wrong.example"
[model_providers.gateway]
base_url = "https://right.example"
requires_openai_auth = false
experimental_bearer_token = "secret"
[tui]
screen_reader_detection_done = true
''')
        self.assertEqual(PLUGIN.base_provider(), {
            "base_url": "https://right.example",
            "requires_openai_auth": False,
            "experimental_bearer_token": "secret",
        })

    def test_refuses_unmanaged_profile(self):
        PLUGIN.profile().parent.mkdir(parents=True)
        PLUGIN.profile().write_text("model = 'mine'\n")
        with self.assertRaisesRegex(RuntimeError, "unmanaged profile"):
            PLUGIN.write_profile(8791)

    def test_setup_bootstraps_release_when_binary_is_missing(self):
        args = type("Args", (), {})()
        process = mock.Mock(pid=4321)
        with mock.patch.object(PLUGIN, "find_binary", return_value=None), \
             mock.patch.object(PLUGIN, "install_release_binary",
                               return_value=("/managed/context-guru-proxy", "v1.2.3")) as install, \
             mock.patch.object(PLUGIN, "first_free_port", return_value=8791), \
             mock.patch.object(PLUGIN, "start_proxy", return_value=process), \
             mock.patch.object(PLUGIN, "install_escape_hatch", return_value=Path("/reset")):
            self.assertEqual(PLUGIN.setup(args), 0)
        install.assert_called_once_with()
        record = json.loads((PLUGIN.state_dir() / "install.json").read_text())
        self.assertEqual(record["binary"], "/managed/context-guru-proxy")

    def test_uninstall_keeps_unmanaged_profile_and_unowned_process(self):
        PLUGIN.profile().parent.mkdir(parents=True)
        PLUGIN.profile().write_text("model = 'mine'\n")
        PLUGIN.state_dir().mkdir(parents=True)
        (PLUGIN.state_dir() / "install.json").write_text(json.dumps({
            "pid": 123, "binary": "/expected", "port": 8791
        }))
        args = type("Args", (), {"dry_run": False})()
        with mock.patch.object(PLUGIN.subprocess, "check_output", return_value="/other --flag"), \
             mock.patch.object(PLUGIN.os, "kill") as kill:
            self.assertEqual(PLUGIN.uninstall(args), 0)
            kill.assert_not_called()
        self.assertTrue(PLUGIN.profile().exists())
        self.assertTrue((PLUGIN.state_dir() / "install.json").exists())

    def test_escape_hatch_is_installed_outside_plugin(self):
        target = PLUGIN.install_escape_hatch()
        self.assertEqual(target, PLUGIN.state_dir() / "context-guru-reset")
        self.assertTrue(os.access(target, os.X_OK))
        self.assertIn("Managed by the context-guru Codex plugin", target.read_text())

    def test_release_binary_is_checksum_verified_and_installed_privately(self):
        payload = b"released proxy"
        archive_buffer = io.BytesIO()
        with tarfile.open(fileobj=archive_buffer, mode="w:gz") as bundle:
            info = tarfile.TarInfo("context-guru_1.2.3_linux_amd64/context-guru-proxy")
            info.size = len(payload)
            bundle.addfile(info, io.BytesIO(payload))
        archive = archive_buffer.getvalue()
        checksum = hashlib.sha256(archive).hexdigest()

        def fake_fetch(url):
            if url.endswith("checksums.txt"):
                return f"{checksum}  context-guru_1.2.3_linux_amd64.tar.gz\n".encode()
            return archive

        with mock.patch.object(PLUGIN, "release_platform", return_value=("linux", "amd64")), \
             mock.patch.object(PLUGIN, "fetch", side_effect=fake_fetch):
            path, version = PLUGIN.install_release_binary("v1.2.3")
        self.assertEqual(version, "v1.2.3")
        self.assertEqual(Path(path), PLUGIN.managed_binary().resolve())
        self.assertEqual(PLUGIN.managed_binary().read_bytes(), payload)
        self.assertTrue(os.access(path, os.X_OK))
        with mock.patch.object(PLUGIN.shutil, "which", return_value=None):
            self.assertEqual(PLUGIN.find_binary(), path)

    def test_release_binary_rejects_checksum_mismatch(self):
        with mock.patch.object(PLUGIN, "release_platform", return_value=("linux", "amd64")), \
             mock.patch.object(PLUGIN, "fetch", side_effect=[b"archive", b"0" * 64 + b"  context-guru_1.2.3_linux_amd64.tar.gz\n"]):
            with self.assertRaisesRegex(RuntimeError, "checksum mismatch"):
                PLUGIN.install_release_binary("v1.2.3")
        self.assertFalse(PLUGIN.managed_binary().exists())

    def test_release_binary_rejects_pre_codex_release(self):
        with mock.patch.object(PLUGIN, "fetch") as fetch:
            with self.assertRaisesRegex(RuntimeError, "predates Codex Responses support"):
                PLUGIN.install_release_binary("v0.3.4")
        fetch.assert_not_called()

    def test_update_check_is_read_only(self):
        args = type("Args", (), {"check": True, "install": False})()
        with mock.patch.object(PLUGIN, "latest_release_tag", return_value="v1.2.3"), \
             mock.patch.object(PLUGIN, "find_binary", return_value=None), \
             mock.patch.object(PLUGIN, "install_release_binary") as install:
            self.assertEqual(PLUGIN.update(args), 0)
        install.assert_not_called()

    def test_reset_dry_run_changes_nothing(self):
        PLUGIN.write_profile(8791)
        PLUGIN.write_proxy_config()
        PLUGIN.state_dir().mkdir(parents=True, exist_ok=True)
        record = PLUGIN.state_dir() / "install.json"
        record.write_text(json.dumps({"port": 8791, "pid": 999999, "binary": "/missing"}))
        env = os.environ.copy()
        completed = subprocess.run(
            ["sh", str(Path(__file__).with_name("reset.sh")), "--dry-run"],
            env=env, text=True, capture_output=True,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("result=planned", completed.stdout)
        self.assertTrue(PLUGIN.profile().exists())
        self.assertTrue(PLUGIN.proxy_config().exists())
        self.assertTrue(record.exists())

    def test_reset_backup_is_private(self):
        PLUGIN.write_profile(8791, {"experimental_bearer_token": "secret"})
        PLUGIN.write_proxy_config()
        completed = subprocess.run(
            ["sh", str(Path(__file__).with_name("reset.sh")), "--yes"],
            env=os.environ.copy(), text=True, capture_output=True,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        backups = list((PLUGIN.state_dir() / "recovery").iterdir())
        self.assertEqual(len(backups), 1)
        self.assertEqual(backups[0].stat().st_mode & 0o777, 0o600)
        self.assertFalse(PLUGIN.proxy_config().exists())

    def test_explicit_update_sets_upgrade_gate(self):
        args = type("Args", (), {"check": False, "install": True})()
        with mock.patch.object(PLUGIN, "latest_release_tag", return_value="v1.2.3"), \
             mock.patch.object(PLUGIN, "find_binary", return_value=None), \
             mock.patch.object(PLUGIN, "install_release_binary",
                               side_effect=RuntimeError("download failed")):
            self.assertEqual(PLUGIN.update(args), 1)


if __name__ == "__main__":
    unittest.main()
