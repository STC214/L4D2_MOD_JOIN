package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"l4d2-mod-join/internal/modscan"
	"l4d2-mod-join/internal/vpkmerge"
)

func TestDynamicMergeIntegration(t *testing.T) {
	source := os.Getenv("L4D2_MOD_JOIN_INTEGRATION_SOURCE")
	if source == "" {
		t.Skip("integration source not configured")
	}
	result, err := modscan.Scan(source)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "merged")
	selections := map[string]string{}
	for _, conflict := range result.Conflicts {
		if !conflict.Identical && !conflict.SafeMerge {
			selections[conflict.Path] = conflict.Packages[0]
		}
	}
	plan, err := result.Plan(output, selections)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := prepareOverlays(&plan, &result)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := vpkmerge.Run(plan, nil); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(output, conflictPolicyName)
	if err := os.WriteFile(policyPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	policyDigest, err := hashFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createBuildManifest(plan, result, policyDigest); err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{}
	for _, group := range plan.Groups {
		info, err := vpkmerge.Inspect(filepath.Join(output, group.Output))
		if err != nil {
			t.Fatalf("%s: %v", group.Output, err)
		}
		for _, file := range info.Files {
			if file.Path == "addoninfo.txt" {
				continue
			}
			if previous := owners[file.Path]; previous != "" {
				t.Fatalf("cross-output duplicate %s in %s and %s", file.Path, previous, group.Output)
			}
			owners[file.Path] = group.Output
		}
	}
}

func TestFindRenamedLocalDuplicateMod(t *testing.T) {
	source := os.Getenv("L4D2_MOD_JOIN_INTEGRATION_SOURCE")
	if source == "" {
		t.Skip("integration source not configured")
	}
	result, err := modscan.Scan(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Packages) == 0 {
		t.Fatal("no source package")
	}
	first := result.Packages[0]
	addons := t.TempDir()
	renamed := "renamed-local-copy.vpk"
	if err := copyFile(first.Path, filepath.Join(addons, renamed)); err != nil {
		t.Fatal(err)
	}
	manifest := buildManifest{SourcePackages: []sourcePackage{{
		Name: filepath.Base(first.Path), Digest: first.Digest, RuntimeSignature: first.RuntimeSignature,
	}}}
	duplicates, err := findLocalDuplicateMods(addons, manifest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(duplicates) != 1 || duplicates[0] != renamed {
		t.Fatalf("renamed duplicate was not detected: %#v", duplicates)
	}
}

func TestDeployDisablesRenamedLocalDuplicateMod(t *testing.T) {
	source := os.Getenv("L4D2_MOD_JOIN_INTEGRATION_SOURCE")
	if source == "" {
		t.Skip("integration source not configured")
	}
	result, err := modscan.Scan(source)
	if err != nil {
		t.Fatal(err)
	}
	first := result.Packages[0]
	root := t.TempDir()
	addons := filepath.Join(root, "left4dead2", "addons")
	output := filepath.Join(root, "merged")
	if err := os.MkdirAll(addons, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		t.Fatal(err)
	}
	localName := "renamed-non-workshop-copy.vpk"
	if err := copyFile(first.Path, filepath.Join(addons, localName)); err != nil {
		t.Fatal(err)
	}
	merged := []byte("merged")
	if err := os.WriteFile(filepath.Join(output, "01_Test.vpk"), merged, 0644); err != nil {
		t.Fatal(err)
	}
	mergedDigest, err := hashFile(filepath.Join(output, "01_Test.vpk"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := buildManifest{
		Version: 2,
		Files: []builtFile{{
			Name: "01_Test.vpk", Size: int64(len(merged)), SHA256: mergedDigest,
		}},
		SourcePackages: []sourcePackage{{
			Name: filepath.Base(first.Path), Digest: first.Digest, RuntimeSignature: first.RuntimeSignature,
		}},
	}
	addonList := filepath.Join(root, "left4dead2", "addonlist.txt")
	input := "\"AddonList\"\r\n{\r\n\t\"" + localName + "\"\t\"1\"\r\n\t\"unrelated-local.vpk\"\t\"1\"\r\n}\r\n"
	if err := os.WriteFile(addonList, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}
	_, duplicates, err := deployAndDisable(manifest, output, addons)
	if err != nil {
		t.Fatal(err)
	}
	if len(duplicates) != 1 || duplicates[0] != localName {
		t.Fatalf("unexpected local duplicates: %#v", duplicates)
	}
	data, err := os.ReadFile(addonList)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "\""+localName+"\"\t\"0\"") {
		t.Fatalf("duplicate local mod was not disabled:\n%s", text)
	}
	if !strings.Contains(text, "\"unrelated-local.vpk\"\t\"1\"") {
		t.Fatalf("unrelated local mod was changed:\n%s", text)
	}
}
