// Tests for #318: a machine-wide install left the plugin ENABLED in one project only.
//
// `/plugin` writes `enabledPlugins`, which decides whether this plugin's commands, skills and hooks
// exist in a session at all; install.sh writes routing, the port option and the status line. Nothing
// bridged the two, so `--scope user` run from the one project the plugin was enabled in routed every
// project on the machine and left `/context-guru:*` missing from all but that one — with the status
// line live and traffic proxied, which made it look like a broken install.
//
// The fix is the `installed_statusline` pattern applied to a second key we own conditionally: write
// `enabledPlugins[<plugin>] = true` into the file the machine-wide install is already editing, never
// over an explicit `false` (a user decision), and record that we added it so uninstall removes it only
// then. The tests below pin each of those three clauses, plus the project-scope boundary.
package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

const pluginID = "context-guru@context-guru"

// enabledIn reads enabledPlugins[pluginID] from a settings file: (value, present).
func enabledIn(t *testing.T, path string) (any, bool) {
	t.Helper()
	ep, _ := readJSON(t, path)["enabledPlugins"].(map[string]any)
	v, ok := ep[pluginID]
	return v, ok
}

// recordedEnable reads the `$context-guru` record of an enablement we added.
func recordedEnable(t *testing.T, path string) any {
	t.Helper()
	meta, _ := readJSON(t, path)["$context-guru"].(map[string]any)
	return meta["installed_enabled_plugin"]
}

// userScopeInstall runs a consented machine-wide install from a project that has the plugin enabled
// in its own settings.local.json only — the exact state the issue was reported from. `seed` is
// merged into ~/.claude/settings.json first (the port option is always added).
func userScopeInstall(t *testing.T, seed map[string]any) (facts map[string]string, home, state, proj string) {
	t.Helper()
	home, state, proj = t.TempDir(), t.TempDir(), t.TempDir()
	port := freePort(t)
	writePluginOptions(t, home, map[string]any{"port": port})
	userFile := filepath.Join(home, ".claude", "settings.json")
	if len(seed) > 0 {
		data := readJSON(t, userFile)
		for k, v := range seed {
			data[k] = v
		}
		writeJSON(t, userFile, data)
	}
	if err := os.MkdirAll(filepath.Join(proj, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(proj, ".claude", "settings.local.json"),
		map[string]any{"enabledPlugins": map[string]any{pluginID: true}})
	env := routeEnv(t, home, state, fakeProxyDir(t, port, true))
	t.Cleanup(func() { stopFakeProxy(t, state, port) })
	facts, code := runRoute(t, proj, env, "--scope", "user", "--i-understand-machine-wide",
		"--i-consent-to-traffic-interception")
	if code != 0 || facts["result"] != "routed" {
		t.Fatalf("the install itself failed, so enablement proves nothing: exit %d %v", code, facts)
	}
	return facts, home, state, proj
}

func TestUserScopeInstallEnablesThePluginMachineWide(t *testing.T) {
	facts, home, _, proj := userScopeInstall(t, map[string]any{
		// Somebody else's plugins, which must come through untouched.
		"enabledPlugins": map[string]any{"pyright-lsp@claude-plugins-official": true},
	})
	userFile := filepath.Join(home, ".claude", "settings.json")
	if v, _ := enabledIn(t, userFile); v != true {
		t.Errorf("enabledPlugins[%s] = %v in the machine-wide file, want true — the commands exist "+
			"only in the project the install was run from", pluginID, v)
	}
	ep, _ := readJSON(t, userFile)["enabledPlugins"].(map[string]any)
	if ep["pyright-lsp@claude-plugins-official"] != true {
		t.Errorf("another plugin's enablement was disturbed: %v", ep)
	}
	if got := recordedEnable(t, userFile); got != pluginID {
		t.Errorf("$context-guru.installed_enabled_plugin = %v, want %q: without the record uninstall "+
			"cannot tell our key from one the user set", got, pluginID)
	}
	// Reported as a key it set — the issue's other half: silence was the defect.
	if facts["plugin_enabled"] != "added" {
		t.Errorf("plugin_enabled=%q, want added: %v", facts["plugin_enabled"], facts)
	}
	// The project's own enablement is the user's, and stays exactly as it was.
	if v, _ := enabledIn(t, filepath.Join(proj, ".claude", "settings.local.json")); v != true {
		t.Errorf("the project's own enablement changed: %v", v)
	}
}

// An explicit `false` in the machine-wide file is a decision the user made in `/plugin`. The install
// must neither flip it nor stay silent about what it means: commands will not appear elsewhere.
func TestUserScopeInstallNeverFlipsAnExplicitFalse(t *testing.T) {
	facts, home, _, _ := userScopeInstall(t, map[string]any{
		"enabledPlugins": map[string]any{pluginID: false},
	})
	userFile := filepath.Join(home, ".claude", "settings.json")
	if v, ok := enabledIn(t, userFile); !ok || v != false {
		t.Errorf("enabledPlugins[%s] = %v (present=%v), want the user's explicit false", pluginID, v, ok)
	}
	if got := recordedEnable(t, userFile); got != nil {
		t.Errorf("recorded an enablement we did not write (%v), so uninstall would delete the "+
			"user's own false", got)
	}
	if facts["plugin_enabled"] != "explicitly_disabled" || facts["plugin_enabled_note"] == "" {
		t.Errorf("plugin_enabled=%q note=%q, want explicitly_disabled with a note",
			facts["plugin_enabled"], facts["plugin_enabled_note"])
	}
}

// Already enabled machine-wide by the user: nothing to write, nothing recorded, and so nothing for
// uninstall to take away.
func TestUserScopeInstallLeavesAnExistingEnableAloneAndUninstallKeepsIt(t *testing.T) {
	facts, home, state, proj := userScopeInstall(t, map[string]any{
		"enabledPlugins": map[string]any{pluginID: true},
	})
	userFile := filepath.Join(home, ".claude", "settings.json")
	if facts["plugin_enabled"] != "already" {
		t.Errorf("plugin_enabled=%q, want already: %v", facts["plugin_enabled"], facts)
	}
	if got := recordedEnable(t, userFile); got != nil {
		t.Errorf("recorded ownership of a key the user set: %v", got)
	}
	out, code := settingsInDir(t, state, home, proj, "remove", "--file", userFile, "--user-scope")
	if code != 0 || out["result"] != "removed" {
		t.Fatalf("uninstall failed: exit %d %v", code, out)
	}
	if v, _ := enabledIn(t, userFile); v != true {
		t.Errorf("uninstall removed an enablement the user set themselves: %v", v)
	}
	if out["plugin_enabled_removed"] != "false" {
		t.Errorf("plugin_enabled_removed=%q, want false", out["plugin_enabled_removed"])
	}
}

// Uninstall takes back exactly what we added, and nothing next to it.
func TestUserScopeUninstallRemovesOnlyTheEnableItAdded(t *testing.T) {
	_, home, state, proj := userScopeInstall(t, map[string]any{
		"enabledPlugins": map[string]any{"pyright-lsp@claude-plugins-official": true},
	})
	userFile := filepath.Join(home, ".claude", "settings.json")
	out, code := settingsInDir(t, state, home, proj, "remove", "--file", userFile, "--user-scope")
	if code != 0 || out["result"] != "removed" {
		t.Fatalf("uninstall failed: exit %d %v", code, out)
	}
	if out["plugin_enabled_removed"] != "true" {
		t.Errorf("plugin_enabled_removed=%q, want true: %v", out["plugin_enabled_removed"], out)
	}
	if _, ok := enabledIn(t, userFile); ok {
		t.Errorf("our enablement survived uninstall")
	}
	ep, _ := readJSON(t, userFile)["enabledPlugins"].(map[string]any)
	if ep["pyright-lsp@claude-plugins-official"] != true {
		t.Errorf("uninstall touched another plugin's enablement: %v", ep)
	}
	if got := recordedEnable(t, userFile); got != nil {
		t.Errorf("the record outlived the key it describes: %v", got)
	}
}

// If the user switched it to false after we enabled it, that is now their decision: keep it.
func TestUserScopeUninstallKeepsAFalseTheUserSetAfterInstall(t *testing.T) {
	_, home, state, proj := userScopeInstall(t, nil)
	userFile := filepath.Join(home, ".claude", "settings.json")
	data := readJSON(t, userFile)
	data["enabledPlugins"].(map[string]any)[pluginID] = false
	writeJSON(t, userFile, data)
	if out, code := settingsInDir(t, state, home, proj, "remove", "--file", userFile,
		"--user-scope"); code != 0 || out["result"] != "removed" {
		t.Fatalf("uninstall failed: exit %d %v", code, out)
	}
	if v, ok := enabledIn(t, userFile); !ok || v != false {
		t.Errorf("uninstall removed a false the user set after install: %v (present=%v)", v, ok)
	}
}

// Re-running the machine-wide install is how a user who installed before this fix repairs their
// machine, so the enablement must be written on the `unchanged` path too, and exactly once.
func TestUserScopeReinstallIsIdempotentAndRepairsAnOlderInstall(t *testing.T) {
	_, home, state, proj := userScopeInstall(t, nil)
	userFile := filepath.Join(home, ".claude", "settings.json")
	// The state an install from before this fix left behind: routed, not enabled, no record.
	data := readJSON(t, userFile)
	delete(data, "enabledPlugins")
	delete(data["$context-guru"].(map[string]any), "installed_enabled_plugin")
	writeJSON(t, userFile, data)

	port := readJSON(t, userFile)["pluginConfigs"].(map[string]any)[pluginID].(map[string]any)["options"].(map[string]any)["port"].(string)
	env := routeEnv(t, home, state, fakeProxyDir(t, port, true))
	for i, want := range []string{"added", "already"} {
		facts, code := runRoute(t, proj, env, "--scope", "user", "--i-understand-machine-wide",
			"--i-consent-to-traffic-interception")
		if code != 0 || facts["result"] != "routed" {
			t.Fatalf("re-run %d failed: exit %d %v", i, code, facts)
		}
		if facts["plugin_enabled"] != want {
			t.Errorf("re-run %d: plugin_enabled=%q, want %q", i, facts["plugin_enabled"], want)
		}
		if v, _ := enabledIn(t, userFile); v != true {
			t.Errorf("re-run %d: not enabled: %v", i, v)
		}
		if got := recordedEnable(t, userFile); got != pluginID {
			t.Errorf("re-run %d: the second run lost the first run's record: %v", i, got)
		}
	}
}

// A project-scope install writes nothing machine-wide — enablement included.
func TestProjectScopeInstallDoesNotEnableMachineWide(t *testing.T) {
	home, state, proj := t.TempDir(), t.TempDir(), t.TempDir()
	port := freePort(t)
	writePluginOptions(t, home, map[string]any{"port": port})
	env := routeEnv(t, home, state, fakeProxyDir(t, port, true))
	t.Cleanup(func() { stopFakeProxy(t, state, port) })
	facts, code := runRoute(t, proj, env, "--scope", "project", "--i-consent-to-traffic-interception")
	if code != 0 || facts["result"] != "routed" {
		t.Fatalf("exit %d: %v", code, facts)
	}
	if _, ok := enabledIn(t, filepath.Join(home, ".claude", "settings.json")); ok {
		t.Errorf("a project-scope install enabled the plugin machine-wide")
	}
	if _, ok := facts["plugin_enabled"]; ok {
		t.Errorf("plugin_enabled reported for a project-scope install: %v", facts)
	}
}

// The scope gate holds for this key as for routing: the machine-wide file needs --user-scope.
func TestEnablePluginRefusesTheUserFileWithoutTheFlag(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	userFile := filepath.Join(home, ".claude", "settings.json")
	writePluginOptions(t, home, map[string]any{})
	out, code := settingsInDir(t, state, home, "", "enable-plugin", "--file", userFile)
	if code != 2 || out["reason"] != "user_scope_needs_flag" {
		t.Errorf("exit %d %v, want the scope refusal", code, out)
	}
	if _, ok := enabledIn(t, userFile); ok {
		t.Errorf("written despite the refusal")
	}
}
