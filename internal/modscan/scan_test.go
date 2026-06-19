package modscan

import (
	"testing"

	"l4d2-mod-join/internal/vpkmerge"
)

func TestPlanRequiresExplicitConflictSelection(t *testing.T) {
	result := Result{
		Directory: "mods",
		Categories: []Category{{
			Key: "misc", Output: "11_Misc.vpk", Title: "Misc",
			Packages: []string{"a.vpk", "b.vpk"},
		}},
		Conflicts: []Conflict{{
			Path: "models/test.mdl", Packages: []string{"a.vpk", "b.vpk"},
		}},
	}
	if _, err := result.Plan("out", nil); err == nil {
		t.Fatal("expected unresolved conflict to block plan")
	}
	plan, err := result.Plan("out", map[string]string{"models/test.mdl": "b.vpk"})
	if err != nil {
		t.Fatal(err)
	}
	excluded := plan.Groups[0].ExcludeByPackage["a.vpk"]
	if len(excluded) != 1 || excluded[0] != "models/test.mdl" {
		t.Fatalf("losing package was not excluded: %#v", excluded)
	}
}

func TestPureIncludeScriptValidation(t *testing.T) {
	if !isPureIncludeScript(`// comment
IncludeScript("one");
IncludeScript( "two" )
`) {
		t.Fatal("pure IncludeScript entry should be safe")
	}
	if isPureIncludeScript(`IncludeScript("one"); function Dangerous() {}`) {
		t.Fatal("mixed script content must not be marked safe")
	}
}

func TestConflictGroupingReducesRepeatedChoices(t *testing.T) {
	result := Result{
		Packages: []vpkmerge.PackageInfo{
			{Path: "a.vpk", Files: []vpkmerge.FileInfo{
				{Path: "materials/a.vtf"}, {Path: "materials/b.vtf"},
			}},
			{Path: "b.vpk", Files: []vpkmerge.FileInfo{
				{Path: "materials/a.vtf"}, {Path: "materials/b.vtf"}, {Path: "models/b.mdl"},
			}},
		},
		Conflicts: []Conflict{
			{Path: "materials/a.vtf", Packages: []string{"a.vpk", "b.vpk"}},
			{Path: "materials/b.vtf", Packages: []string{"a.vpk", "b.vpk"}},
		},
	}
	groups := buildConflictGroups(&result)
	if len(groups) != 1 {
		t.Fatalf("expected one grouped decision, got %d", len(groups))
	}
	if !groups[0].AutoResolved || groups[0].Recommended != "b.vpk" {
		t.Fatalf("expected strict superset to auto-resolve to b.vpk: %#v", groups[0])
	}
	if result.Conflicts[0].AutoWinner != "b.vpk" || result.Conflicts[1].AutoWinner != "b.vpk" {
		t.Fatal("auto winner was not propagated to grouped conflicts")
	}
}

func TestMiscPackagesStayInOneOutputGroup(t *testing.T) {
	result := Result{Categories: []Category{{
		Key: "misc", Output: "11_Misc.vpk", Title: "Misc",
		Packages: []string{"one.vpk", "two.vpk", "three.vpk"},
	}}}
	plan, err := result.Plan("out", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 1 || len(plan.Groups[0].Packages) != 3 {
		t.Fatalf("misc packages were not kept in one output: %#v", plan.Groups)
	}
}

func TestMaterialOnlyCompanionFollowsUniqueNamespaceCategory(t *testing.T) {
	packages := []vpkmerge.PackageInfo{
		{Path: "weapon.vpk", Files: []vpkmerge.FileInfo{
			{Path: "models/v_models/v_rifle.mdl"},
			{Path: "materials/mw2019/weapons/rifle.vtf"},
		}},
		{Path: "screen-patch.vpk", Files: []vpkmerge.FileInfo{
			{Path: "materials/mw2019/screen/display.vmt"},
			{Path: "materials/mw2019/screen/display.vtf"},
		}},
	}
	categories := map[string]string{"weapon.vpk": "weapons", "screen-patch.vpk": "misc"}
	inferred := refineMaterialCompanions(packages, categories)
	if categories["screen-patch.vpk"] != "weapons" || len(inferred) != 1 {
		t.Fatalf("material companion was not assigned to weapons: %#v %#v", categories, inferred)
	}
}

func TestMaterialOnlyCompanionStaysMiscForAmbiguousOrGenericNamespace(t *testing.T) {
	packages := []vpkmerge.PackageInfo{
		{Path: "weapon.vpk", Files: []vpkmerge.FileInfo{
			{Path: "models/v_models/v_rifle.mdl"},
			{Path: "materials/shared/rifle.vtf"},
			{Path: "materials/models/weapons/rifle.vtf"},
		}},
		{Path: "survivor.vpk", Files: []vpkmerge.FileInfo{
			{Path: "models/survivors/survivor_test.mdl"},
			{Path: "materials/shared/survivor.vtf"},
		}},
		{Path: "ambiguous.vpk", Files: []vpkmerge.FileInfo{{Path: "materials/shared/patch.vtf"}}},
		{Path: "generic.vpk", Files: []vpkmerge.FileInfo{{Path: "materials/models/patch.vtf"}}},
	}
	categories := map[string]string{
		"weapon.vpk": "weapons", "survivor.vpk": "survivors",
		"ambiguous.vpk": "misc", "generic.vpk": "misc",
	}
	if inferred := refineMaterialCompanions(packages, categories); len(inferred) != 0 {
		t.Fatalf("ambiguous/generic companions should remain misc: %#v", inferred)
	}
}
