package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeployDisablesOnlyPlannedMods(t *testing.T) {
	root := t.TempDir()
	addons := filepath.Join(root, "left4dead2", "addons")
	output := filepath.Join(root, "merged")
	stateDir := filepath.Join(root, "app")
	if err := os.MkdirAll(addons, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		t.Fatal(err)
	}
	content := []byte("merged")
	outputFile := filepath.Join(output, "01_Test.vpk")
	if err := os.WriteFile(outputFile, content, 0644); err != nil {
		t.Fatal(err)
	}
	digest, err := hashFile(outputFile)
	if err != nil {
		t.Fatal(err)
	}
	manifest := buildManifest{
		Version: 2, Packages: []string{"123.vpk"},
		Files: []builtFile{{Name: "01_Test.vpk", Size: int64(len(content)), SHA256: digest}},
	}
	addonList := filepath.Join(root, "left4dead2", "addonlist.txt")
	input := "\"AddonList\"\r\n{\r\n\t\"workshop\\123.vpk\"\t\"1\"\r\n\t\"workshop\\999.vpk\"\t\"1\"\r\n}\r\n"
	if err := os.WriteFile(addonList, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}
	var progressValues []int64
	if _, _, err := deployAndDisable(manifest, output, addons, stateDir, func(current, total int64, _ string) {
		if total > 0 {
			progressValues = append(progressValues, current*100/total)
		}
	}); err != nil {
		t.Fatal(err)
	}
	assertProgressCompletes(t, progressValues)
	if _, err := os.Stat(filepath.Join(stateDir, deploymentManifestName)); err != nil {
		t.Fatalf("deployment manifest was not stored beside the app: %v", err)
	}
	if _, err := os.Stat(filepath.Join(addons, deploymentManifestName)); !os.IsNotExist(err) {
		t.Fatalf("deployment manifest was written into addons: %v", err)
	}
	data, err := os.ReadFile(addonList)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "\"workshop\\123.vpk\"\t\"0\"") {
		t.Fatalf("planned mod was not disabled:\n%s", text)
	}
	if !strings.Contains(text, "\"workshop\\999.vpk\"\t\"1\"") {
		t.Fatalf("unrelated mod was changed:\n%s", text)
	}
	if !strings.Contains(text, "\"01_Test.vpk\"\t\t\"1\"") {
		t.Fatalf("merged mod was not enabled:\n%s", text)
	}
}

func TestDeployDisablesAndMovesStaleManagedOutputs(t *testing.T) {
	root := t.TempDir()
	addons := filepath.Join(root, "left4dead2", "addons")
	output := filepath.Join(root, "merged")
	stateDir := filepath.Join(root, "app")
	if err := os.MkdirAll(addons, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(addons, "12_Training_Map.vpk"), []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(addons, deploymentManifestName), buildManifest{
		Version: 2, Files: []builtFile{{Name: "12_Training_Map.vpk"}},
	}); err != nil {
		t.Fatal(err)
	}
	current := []byte("current")
	if err := os.WriteFile(filepath.Join(output, "01_UI_HUD.vpk"), current, 0644); err != nil {
		t.Fatal(err)
	}
	digest, _ := hashFile(filepath.Join(output, "01_UI_HUD.vpk"))
	manifest := buildManifest{
		Version: 2, Packages: []string{"123.vpk"},
		Files: []builtFile{{Name: "01_UI_HUD.vpk", Size: int64(len(current)), SHA256: digest}},
	}
	addonList := filepath.Join(root, "left4dead2", "addonlist.txt")
	input := "\"AddonList\"\r\n{\r\n\t\"12_Training_Map.vpk\"\t\"1\"\r\n\t\"workshop\\123.vpk\"\t\"1\"\r\n}\r\n"
	if err := os.WriteFile(addonList, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := deployAndDisable(manifest, output, addons, stateDir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, deploymentManifestName)); err != nil {
		t.Fatalf("legacy deployment manifest was not migrated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(addons, deploymentManifestName)); !os.IsNotExist(err) {
		t.Fatalf("legacy deployment manifest remains in addons: %v", err)
	}
	if _, err := os.Stat(filepath.Join(addons, "12_Training_Map.vpk")); !os.IsNotExist(err) {
		t.Fatal("stale managed output was left in addons")
	}
	data, _ := os.ReadFile(addonList)
	if !strings.Contains(string(data), "\"12_Training_Map.vpk\"\t\"0\"") {
		t.Fatalf("stale output was not disabled:\n%s", data)
	}
	backups, _ := filepath.Glob(filepath.Join(root, "left4dead2", "l4d2modjoin_backup", "*", "12_Training_Map.vpk"))
	if len(backups) != 1 {
		t.Fatalf("stale output was not retained outside addons: %#v", backups)
	}
}

func TestRestorePreservesDisabledBaselineEntries(t *testing.T) {
	root := t.TempDir()
	left4dead2 := filepath.Join(root, "left4dead2")
	addons := filepath.Join(left4dead2, "addons")
	stateDir := filepath.Join(root, "app")
	if err := os.MkdirAll(addons, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(stateDir, deploymentManifestName), buildManifest{
		Version:        2,
		Packages:       []string{"disabled-local.vpk"},
		Files:          []builtFile{{Name: "01_UI_HUD.vpk"}},
		DeployedAddons: cleanPath(addons),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(addons, "01_UI_HUD.vpk"), []byte("merged"), 0644); err != nil {
		t.Fatal(err)
	}
	addonList := filepath.Join(left4dead2, "addonlist.txt")
	if err := os.WriteFile(addonList, []byte("\"AddonList\"\r\n{\r\n}\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	baseline := "\"AddonList\"\r\n{\r\n\t\"disabled-local.vpk\"\t\"0\"\r\n}\r\n"
	backup := addonList + ".l4d2modjoin.20260101-000000.000000000.bak"
	if err := os.WriteFile(backup, []byte(baseline), 0644); err != nil {
		t.Fatal(err)
	}
	var progressValues []int64
	if _, err := restoreLatest(addons, stateDir, func(current, total int64, _ string) {
		if total > 0 {
			progressValues = append(progressValues, current*100/total)
		}
	}); err != nil {
		t.Fatal(err)
	}
	assertProgressCompletes(t, progressValues)
	data, err := os.ReadFile(addonList)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != baseline {
		t.Fatalf("disabled baseline entry changed:\n%s", data)
	}
}

func assertProgressCompletes(t *testing.T, values []int64) {
	t.Helper()
	if len(values) == 0 || values[len(values)-1] != 100 {
		t.Fatalf("progress did not reach 100: %#v", values)
	}
	for index := 1; index < len(values); index++ {
		if values[index] < values[index-1] {
			t.Fatalf("progress moved backwards: %#v", values)
		}
	}
}

func TestRestoreMovesCurrentMergedPackagesOutOfAddons(t *testing.T) {
	root := t.TempDir()
	left4dead2 := filepath.Join(root, "left4dead2")
	addons := filepath.Join(left4dead2, "addons")
	stateDir := filepath.Join(root, "app")
	if err := os.MkdirAll(addons, 0755); err != nil {
		t.Fatal(err)
	}
	merged := filepath.Join(addons, "01_UI_HUD.vpk")
	if err := os.WriteFile(merged, []byte("merged"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(stateDir, deploymentManifestName), buildManifest{
		Version: 2, Files: []builtFile{{Name: "01_UI_HUD.vpk"}}, DeployedAddons: cleanPath(addons),
	}); err != nil {
		t.Fatal(err)
	}
	addonList := filepath.Join(left4dead2, "addonlist.txt")
	if err := os.WriteFile(addonList, []byte("\"AddonList\"\r\n{\r\n}\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	backup := addonList + ".l4d2modjoin.20260101-000000.000000000.bak"
	original := "\"AddonList\"\r\n{\r\n\t\"workshop\\123.vpk\"\t\"1\"\r\n}\r\n"
	if err := os.WriteFile(backup, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreLatest(addons, stateDir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(merged); !os.IsNotExist(err) {
		t.Fatal("current merged package remains in addons after restore")
	}
	data, _ := os.ReadFile(addonList)
	if string(data) != original {
		t.Fatalf("addonlist was not restored:\n%s", data)
	}
}

func TestRestoreBlocksWhenDeploymentRecordMissingOrCorrupt(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "corrupt"}[corrupt], func(t *testing.T) {
			root := t.TempDir()
			left4dead2 := filepath.Join(root, "left4dead2")
			addons := filepath.Join(left4dead2, "addons")
			stateDir := filepath.Join(root, "app")
			if err := os.MkdirAll(addons, 0755); err != nil {
				t.Fatal(err)
			}
			merged := filepath.Join(addons, "01_UI_HUD.vpk")
			if err := os.WriteFile(merged, []byte("merged"), 0644); err != nil {
				t.Fatal(err)
			}
			addonList := filepath.Join(left4dead2, "addonlist.txt")
			current := "\"AddonList\"\r\n{\r\n\t\"01_UI_HUD.vpk\"\t\"1\"\r\n}\r\n"
			if err := os.WriteFile(addonList, []byte(current), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(addonList+".l4d2modjoin.20260101-000000.000000000.bak",
				[]byte("\"AddonList\"\r\n{\r\n}\r\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if corrupt {
				if err := os.MkdirAll(stateDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stateDir, deploymentManifestName), []byte("{broken"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := restoreLatest(addons, stateDir, nil); err == nil {
				t.Fatal("restore should be blocked without a valid deployment record")
			}
			if _, err := os.Stat(merged); err != nil {
				t.Fatal("merged package was changed despite blocked restore")
			}
			data, _ := os.ReadFile(addonList)
			if string(data) != current {
				t.Fatalf("addonlist changed despite blocked restore:\n%s", data)
			}
		})
	}
}

func TestDeploymentRegistryKeepsMultipleAddonsDirectories(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "app")
	output := filepath.Join(root, "merged")
	if err := os.MkdirAll(output, 0755); err != nil {
		t.Fatal(err)
	}
	content := []byte("merged")
	if err := os.WriteFile(filepath.Join(output, "01_Test.vpk"), content, 0644); err != nil {
		t.Fatal(err)
	}
	digest, _ := hashFile(filepath.Join(output, "01_Test.vpk"))
	manifest := buildManifest{
		Version: 2,
		Files:   []builtFile{{Name: "01_Test.vpk", Size: int64(len(content)), SHA256: digest}},
	}
	var addonsDirs []string
	for _, name := range []string{"game-a", "game-b"} {
		addons := filepath.Join(root, name, "left4dead2", "addons")
		if err := os.MkdirAll(addons, 0755); err != nil {
			t.Fatal(err)
		}
		addonList := filepath.Join(filepath.Dir(addons), "addonlist.txt")
		if err := os.WriteFile(addonList, []byte("\"AddonList\"\r\n{\r\n}\r\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := deployAndDisable(manifest, output, addons, stateDir, nil); err != nil {
			t.Fatal(err)
		}
		addonsDirs = append(addonsDirs, addons)
	}
	registry, err := loadDeploymentRegistry(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Deployments) != 2 {
		t.Fatalf("expected two deployment records, got %#v", registry.Deployments)
	}
	for _, addons := range addonsDirs {
		if _, found := registryDeployment(registry, addons); !found {
			t.Fatalf("missing deployment record for %s", addons)
		}
	}
}

func TestLegacyDeploymentManifestInfersAddonsFromWorkshopSource(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "app")
	addons := filepath.Join(root, "left4dead2", "addons")
	legacy := buildManifest{
		Version: 2,
		Source:  filepath.Join(addons, "workshop"),
		Files:   []builtFile{{Name: "01_Test.vpk"}},
	}
	if err := writeJSONAtomic(filepath.Join(stateDir, deploymentManifestName), legacy); err != nil {
		t.Fatal(err)
	}
	registry, err := loadDeploymentRegistry(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	deployment, found := registryDeployment(registry, addons)
	if !found || deployment.DeployedAddons != cleanPath(addons) {
		t.Fatalf("legacy deployment ownership was not inferred: %#v", registry)
	}
}

func TestMigrateDeploymentRegistryRewritesLegacyFile(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "app")
	addons := filepath.Join(root, "left4dead2", "addons")
	if err := writeJSONAtomic(filepath.Join(stateDir, deploymentManifestName), buildManifest{
		Version: 2,
		Source:  filepath.Join(addons, "workshop"),
		Files:   []builtFile{{Name: "01_Test.vpk"}},
	}); err != nil {
		t.Fatal(err)
	}
	migrated, err := migrateDeploymentRegistry(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatal("legacy deployment file was not migrated")
	}
	data, err := os.ReadFile(filepath.Join(stateDir, deploymentManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var registry deploymentRegistry
	if err := json.Unmarshal(data, &registry); err != nil {
		t.Fatal(err)
	}
	if len(registry.Deployments) != 1 {
		t.Fatalf("unexpected migrated registry: %#v", registry)
	}
}
