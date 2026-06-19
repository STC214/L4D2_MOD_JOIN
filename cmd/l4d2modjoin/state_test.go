package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"l4d2-mod-join/internal/modscan"
	"l4d2-mod-join/internal/vpkmerge"
)

func TestConflictPolicyBlocksUnresolvedChoices(t *testing.T) {
	output := t.TempDir()
	result := modscan.Result{
		Fingerprint: "fingerprint",
		Conflicts: []modscan.Conflict{{
			Path: "models/test.mdl", Packages: []string{"a.vpk", "b.vpk"},
		}},
	}
	if err := writeConflictPolicy(output, result); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConflictSelections(output, result.Fingerprint); err == nil {
		t.Fatal("unresolved policy should block merge")
	}
	path := filepath.Join(output, conflictPolicyName)
	data, _ := os.ReadFile(path)
	var policy conflictPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		t.Fatal(err)
	}
	policy.Conflicts[0].Selected = "b.vpk"
	if err := writeJSONAtomic(path, policy); err != nil {
		t.Fatal(err)
	}
	selections, err := loadConflictSelections(output, result.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if selections["models/test.mdl"] != "b.vpk" {
		t.Fatalf("unexpected selection: %#v", selections)
	}
}

func TestGroupedSelectionAppliesToEveryPath(t *testing.T) {
	output := t.TempDir()
	result := modscan.Result{
		Fingerprint: "grouped",
		Conflicts: []modscan.Conflict{
			{Path: "materials/a.vtf", Packages: []string{"a.vpk", "b.vpk"}},
			{Path: "materials/b.vtf", Packages: []string{"a.vpk", "b.vpk"}},
		},
		ConflictGroups: []modscan.ConflictGroup{{
			ID: "group", Packages: []string{"a.vpk", "b.vpk"},
			Paths:       []string{"materials/a.vtf", "materials/b.vtf"},
			Recommended: "b.vpk",
		}},
	}
	if err := writeConflictPolicy(output, result); err != nil {
		t.Fatal(err)
	}
	selections := map[string]string{
		"materials/a.vtf": "b.vpk",
		"materials/b.vtf": "b.vpk",
	}
	if err := saveConflictGroupSelections(output, result, selections); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConflictSelections(output, result.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if loaded["materials/a.vtf"] != "b.vpk" || loaded["materials/b.vtf"] != "b.vpk" {
		t.Fatalf("group selection was not applied to all paths: %#v", loaded)
	}
}

func TestRemoveLegacyOutputJSONOnlyDeletesMatchingToolState(t *testing.T) {
	output := t.TempDir()
	stateDir := t.TempDir()
	matching := conflictPolicy{Fingerprint: "same"}
	if err := writeJSONAtomic(filepath.Join(output, conflictPolicyName), matching); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(stateDir, conflictPolicyName), matching); err != nil {
		t.Fatal(err)
	}
	unrelated := []byte(`{"user":"keep me"}`)
	if err := os.WriteFile(filepath.Join(output, scanReportName), unrelated, 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(stateDir, scanReportName), modscan.Result{Fingerprint: "current"}); err != nil {
		t.Fatal(err)
	}
	if err := removeLegacyOutputJSON(output, stateDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(output, conflictPolicyName)); !os.IsNotExist(err) {
		t.Fatal("matching tool JSON was not removed")
	}
	data, err := os.ReadFile(filepath.Join(output, scanReportName))
	if err != nil || string(data) != string(unrelated) {
		t.Fatal("unrelated same-name JSON was removed or changed")
	}
}

func TestSubscriptionChangesUsesSourcePackages(t *testing.T) {
	result := modscan.Result{Packages: []vpkmerge.PackageInfo{
		{Path: `mods\a.vpk`}, {Path: `mods\new.vpk`},
	}}
	deployment := buildManifest{
		Packages: []string{"a.vpk", "local-duplicate.vpk"},
		SourcePackages: []sourcePackage{
			{Name: "a.vpk"}, {Name: "removed.vpk"},
		},
	}
	added, removed := subscriptionChanges(result, deployment)
	if len(added) != 1 || added[0] != "new.vpk" ||
		len(removed) != 1 || removed[0] != "removed.vpk" {
		t.Fatalf("unexpected subscription changes: added=%#v removed=%#v", added, removed)
	}
}

func TestAppSettingsRoundTrip(t *testing.T) {
	stateDir := t.TempDir()
	expected := appSettings{
		Source: `E:\SteamLibrary\steamapps\common\Left 4 Dead 2\left4dead2\addons\workshop`,
		Output: `D:\L4D2\merged`,
		Addons: `E:\SteamLibrary\steamapps\common\Left 4 Dead 2\left4dead2\addons`,
	}
	if err := saveAppSettings(stateDir, expected); err != nil {
		t.Fatal(err)
	}
	actual, err := loadAppSettings(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Version != 1 || actual.Source != expected.Source ||
		actual.Output != expected.Output || actual.Addons != expected.Addons {
		t.Fatalf("settings did not round trip: %#v", actual)
	}
}

func TestLoadAppSettingsRejectsCorruptOrUnknownVersion(t *testing.T) {
	for name, content := range map[string]string{
		"corrupt": `{broken`,
		"version": `{"version":99}`,
	} {
		t.Run(name, func(t *testing.T) {
			stateDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(stateDir, settingsName), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := loadAppSettings(stateDir); err == nil {
				t.Fatal("invalid settings should be rejected")
			}
		})
	}
}

func TestMigrateRootStateFiles(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, stateDirectoryName)
	for _, name := range []string{scanReportName, settingsName, deploymentManifestName} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	count, err := migrateRootStateFiles(root, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("unexpected migrated count: %d", count)
	}
	for _, name := range []string{scanReportName, settingsName, deploymentManifestName} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("top-level JSON remains: %s", name)
		}
		if _, err := os.Stat(filepath.Join(stateDir, name)); err != nil {
			t.Fatalf("migrated JSON missing: %s: %v", name, err)
		}
	}
}

func TestMigrateRootStateFilesPreservesConflict(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, stateDirectoryName)
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, settingsName)
	destination := filepath.Join(stateDir, settingsName)
	if err := os.WriteFile(source, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateRootStateFiles(root, stateDir); err == nil {
		t.Fatal("conflicting JSON should be reported")
	}
	if data, _ := os.ReadFile(source); string(data) != "old" {
		t.Fatal("conflicting top-level JSON was overwritten")
	}
	if data, _ := os.ReadFile(destination); string(data) != "new" {
		t.Fatal("data JSON was overwritten")
	}
}
