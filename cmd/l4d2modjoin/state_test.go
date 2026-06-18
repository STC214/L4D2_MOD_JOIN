package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"l4d2-mod-join/internal/modscan"
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
