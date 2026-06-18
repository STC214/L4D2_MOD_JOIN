package modscan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"l4d2-mod-join/internal/vpkmerge"
)

type Category struct {
	Key      string
	Output   string
	Title    string
	Packages []string
}

type Conflict struct {
	Path       string
	Packages   []string
	Identical  bool
	SafeMerge  bool
	AutoWinner string
	Reason     string
}

type ConflictGroup struct {
	ID           string
	Packages     []string
	Paths        []string
	Recommended  string
	Reason       string
	AutoResolved bool
}

type Result struct {
	Directory       string
	Packages        []vpkmerge.PackageInfo
	Categories      []Category
	Conflicts       []Conflict
	ConflictGroups  []ConflictGroup
	UnknownPackages []string
	Fingerprint     string
}

type ownerEntry struct {
	name string
	crc  uint32
}

var includeScriptLine = regexp.MustCompile(`(?i)^IncludeScript\s*\(\s*"[^"]+"\s*\)\s*;?\s*$`)
var manifestPairLine = regexp.MustCompile(`^\s*"[^"]+"\s+"[^"]+"\s*$`)
var archiveChunkName = regexp.MustCompile(`(?i)_\d{3}\.vpk$`)

var categoryOrder = []struct {
	key, output, title string
}{
	{"ui", "01_UI_HUD.vpk", "Merged UI and HUD"},
	{"survivors", "02_Survivors.vpk", "Merged Survivor Models"},
	{"infected", "03_Infected.vpk", "Merged Infected Models"},
	{"weapons", "04_Weapons.vpk", "Merged Weapons"},
	{"environment", "05_Environment.vpk", "Merged Environment"},
	{"effects", "06_Effects.vpk", "Merged Visual Effects"},
	{"audio", "07_Audio.vpk", "Merged Audio"},
	{"gameplay", "08_Gameplay.vpk", "Merged Gameplay Scripts"},
	{"sprays", "09_Sprays.vpk", "Merged Sprays"},
	{"maps", "10_Maps.vpk", "Merged Maps and Campaigns"},
	{"misc", "11_Misc.vpk", "Merged Miscellaneous"},
}

func Scan(directory string) (Result, error) {
	paths, err := filepath.Glob(filepath.Join(directory, "*.vpk"))
	if err != nil {
		return Result{}, err
	}
	if len(paths) == 0 {
		return Result{}, fmt.Errorf("目录中没有 VPK 文件")
	}
	sort.Strings(paths)
	filtered := paths[:0]
	for _, path := range paths {
		if archiveChunkName.MatchString(filepath.Base(path)) {
			continue
		}
		filtered = append(filtered, path)
	}
	paths = filtered
	if len(paths) == 0 {
		return Result{}, fmt.Errorf("目录中没有可读取的 VPK 主包")
	}
	result := Result{Directory: directory}
	owners := map[string][]ownerEntry{}
	packageCategory := map[string]string{}
	for _, path := range paths {
		if isToolGenerated(path) {
			continue
		}
		info, inspectErr := vpkmerge.Inspect(path)
		if inspectErr != nil {
			return Result{}, fmt.Errorf("%s: %w", filepath.Base(path), inspectErr)
		}
		result.Packages = append(result.Packages, info)
		category := classify(info)
		name := filepath.Base(path)
		packageCategory[name] = category
		if category == "misc" {
			result.UnknownPackages = append(result.UnknownPackages, name)
		}
		for _, file := range info.Files {
			if isMetadata(file.Path) || strings.HasPrefix(file.Path, "source files/") {
				continue
			}
			owners[file.Path] = append(owners[file.Path], ownerEntry{name: name, crc: file.CRC})
		}
	}
	if len(result.Packages) == 0 {
		return Result{}, fmt.Errorf("目录中没有未合并的源 MOD")
	}
	for path, entries := range owners {
		if len(entries) < 2 {
			continue
		}
		conflict := Conflict{Path: path, Identical: true}
		for index, entry := range entries {
			conflict.Packages = append(conflict.Packages, entry.name)
			if index > 0 && entry.crc != entries[0].crc {
				conflict.Identical = false
			}
		}
		if !conflict.Identical {
			conflict.SafeMerge = isSafeSemanticMerge(directory, conflict.Path, conflict.Packages)
		}
		result.Conflicts = append(result.Conflicts, conflict)
	}
	categoryPackages := map[string][]string{}
	for _, info := range result.Packages {
		name := filepath.Base(info.Path)
		categoryPackages[packageCategory[name]] = append(categoryPackages[packageCategory[name]], name)
	}
	for _, definition := range categoryOrder {
		names := categoryPackages[definition.key]
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		result.Categories = append(result.Categories, Category{
			Key: definition.key, Output: definition.output, Title: definition.title, Packages: names,
		})
	}
	sort.Slice(result.Conflicts, func(i, j int) bool { return result.Conflicts[i].Path < result.Conflicts[j].Path })
	result.ConflictGroups = buildConflictGroups(&result)
	result.Fingerprint = fingerprint(result.Packages)
	return result, nil
}

func (result Result) Plan(output string, selections map[string]string) (vpkmerge.Plan, error) {
	plan := vpkmerge.Plan{Input: result.Directory, Output: output}
	groupByPackage := map[string]int{}
	for _, category := range result.Categories {
		group := vpkmerge.Group{Output: category.Output, Title: category.Title, Packages: append([]string(nil), category.Packages...)}
		plan.Groups = append(plan.Groups, group)
		groupIndex := len(plan.Groups) - 1
		for _, packageName := range category.Packages {
			groupByPackage[packageName] = groupIndex
		}
	}
	for _, conflict := range result.Conflicts {
		winner := ""
		if conflict.Identical || conflict.SafeMerge {
			winner = conflict.Packages[0]
		} else if conflict.AutoWinner != "" {
			winner = conflict.AutoWinner
		} else {
			winner = selections[conflict.Path]
			if !contains(conflict.Packages, winner) {
				return vpkmerge.Plan{}, fmt.Errorf("冲突 %s 尚未选择保留来源", conflict.Path)
			}
		}
		for _, packageName := range conflict.Packages {
			if packageName == winner {
				continue
			}
			index := groupByPackage[packageName]
			if plan.Groups[index].ExcludeByPackage == nil {
				plan.Groups[index].ExcludeByPackage = map[string][]string{}
			}
			plan.Groups[index].ExcludeByPackage[packageName] = append(
				plan.Groups[index].ExcludeByPackage[packageName], conflict.Path,
			)
		}
	}
	return plan, nil
}

func buildConflictGroups(result *Result) []ConflictGroup {
	packageInfo := map[string]vpkmerge.PackageInfo{}
	for _, info := range result.Packages {
		packageInfo[filepath.Base(info.Path)] = info
	}
	grouped := map[string]*ConflictGroup{}
	for index := range result.Conflicts {
		conflict := &result.Conflicts[index]
		if conflict.Identical || conflict.SafeMerge {
			continue
		}
		packages := append([]string(nil), conflict.Packages...)
		sort.Strings(packages)
		key := strings.Join(packages, "\x00")
		group := grouped[key]
		if group == nil {
			group = &ConflictGroup{
				ID: shortID(key), Packages: packages,
			}
			grouped[key] = group
		}
		group.Paths = append(group.Paths, conflict.Path)
	}
	var groups []ConflictGroup
	for _, group := range grouped {
		group.Recommended, group.Reason, group.AutoResolved = recommendGroup(*group, packageInfo)
		if group.AutoResolved {
			for index := range result.Conflicts {
				conflict := &result.Conflicts[index]
				if sameStrings(conflict.Packages, group.Packages) && contains(group.Paths, conflict.Path) {
					conflict.AutoWinner = group.Recommended
					conflict.Reason = group.Reason
				}
			}
		}
		sort.Strings(group.Paths)
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].AutoResolved != groups[j].AutoResolved {
			return groups[i].AutoResolved
		}
		return groups[i].ID < groups[j].ID
	})
	return groups
}

func recommendGroup(group ConflictGroup, infos map[string]vpkmerge.PackageInfo) (string, string, bool) {
	type score struct {
		name      string
		fileCount int
		covers    int
	}
	var scores []score
	for _, name := range group.Packages {
		info := infos[name]
		paths := map[string]bool{}
		for _, file := range info.Files {
			paths[file.Path] = true
		}
		covers := 0
		for _, path := range group.Paths {
			if paths[path] {
				covers++
			}
		}
		scores = append(scores, score{name: name, fileCount: len(info.Files), covers: covers})
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].covers != scores[j].covers {
			return scores[i].covers > scores[j].covers
		}
		if scores[i].fileCount != scores[j].fileCount {
			return scores[i].fileCount > scores[j].fileCount
		}
		return scores[i].name < scores[j].name
	})
	if len(scores) == 0 {
		return "", "没有可用来源", false
	}
	recommended := scores[0].name
	reason := fmt.Sprintf("推荐保留 %s：它在该冲突组中资源更完整（共 %d 个包内文件）。",
		recommended, scores[0].fileCount)

	// A strict superset is safe to resolve automatically: every runtime file
	// from the smaller package is already present in the larger package.
	recommendedPaths := filePathSet(infos[recommended])
	strictSuperset := len(scores) > 1
	for _, candidate := range scores[1:] {
		otherPaths := filePathSet(infos[candidate.name])
		if len(recommendedPaths) <= len(otherPaths) || !setContains(recommendedPaths, otherPaths) {
			strictSuperset = false
			break
		}
	}
	if strictSuperset {
		return recommended, "自动处理：该 MOD 完整包含其他竞争 MOD 的全部运行时资源。", true
	}
	return recommended, reason, false
}

func filePathSet(info vpkmerge.PackageInfo) map[string]bool {
	result := map[string]bool{}
	for _, file := range info.Files {
		if !isMetadata(file.Path) && !strings.HasPrefix(file.Path, "source files/") {
			result[file.Path] = true
		}
	}
	return result
}

func setContains(left, right map[string]bool) bool {
	for path := range right {
		if !left[path] {
			return false
		}
	}
	return true
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	a, b := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(a)
	sort.Strings(b)
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func shortID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:6])
}

func classify(info vpkmerge.PackageInfo) string {
	flags := map[string]bool{}
	for _, file := range info.Files {
		path := file.Path
		switch {
		case strings.HasPrefix(path, "maps/") || strings.HasPrefix(path, "missions/"):
			flags["maps"] = true
		case strings.HasPrefix(path, "models/survivors/survivor_") ||
			(strings.HasPrefix(path, "models/weapons/arms/") && strings.Contains(path, "v_arms_")):
			flags["survivors"] = true
		case strings.HasPrefix(path, "models/infected/") || strings.Contains(path, "v_claw_"):
			flags["infected"] = true
		case strings.HasPrefix(path, "scripts/weapon_") || strings.HasPrefix(path, "models/weapons/") ||
			strings.HasPrefix(path, "models/v_models/") || strings.HasPrefix(path, "models/w_models/"):
			flags["weapons"] = true
		case strings.HasPrefix(path, "resource/") || strings.HasPrefix(path, "materials/vgui/") ||
			path == "scripts/hudlayout.res":
			flags["ui"] = true
		case path == "scripts/sprays_manifest.txt" || strings.Contains(path, "/sprays/"):
			flags["sprays"] = true
		case strings.HasPrefix(path, "particles/"):
			flags["effects"] = true
		case strings.HasPrefix(path, "sound/"):
			flags["audio"] = true
		case strings.HasPrefix(path, "scripts/vscripts/") || strings.HasPrefix(path, "scripts/talker/"):
			flags["gameplay"] = true
		case strings.HasPrefix(path, "materials/models/") || strings.HasPrefix(path, "models/props") ||
			strings.Contains(path, "skybox") || strings.Contains(path, "water"):
			flags["environment"] = true
		}
	}
	// Strong runtime identity markers take precedence over supporting textures,
	// sounds and particles contained in the same package.
	for _, key := range []string{
		"maps", "survivors", "infected", "weapons", "sprays",
		"ui", "gameplay", "effects", "audio", "environment",
	} {
		if flags[key] {
			return key
		}
	}
	return "misc"
}

func isSafeSemanticMerge(directory, path string, packages []string) bool {
	switch path {
	case "scripts/vscripts/director_base_addon.nut":
		for _, name := range packages {
			content, err := vpkmerge.ReadFile(filepath.Join(directory, name), path)
			if err != nil || !isPureIncludeScript(string(content)) {
				return false
			}
		}
		return true
	case "scripts/sprays_manifest.txt":
		for _, name := range packages {
			content, err := vpkmerge.ReadFile(filepath.Join(directory, name), path)
			if err != nil {
				return false
			}
			text := string(content)
			if strings.Count(text, "{") != 1 || strings.Count(text, "}") != 1 {
				return false
			}
			start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
			for _, line := range strings.Split(text[start+1:end], "\n") {
				line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
				if line == "" || strings.HasPrefix(line, "//") {
					continue
				}
				if !manifestPairLine.MatchString(line) {
					return false
				}
			}
		}
		return true
	default:
		return false
	}
}

func isPureIncludeScript(content string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if !includeScriptLine.MatchString(line) {
			return false
		}
	}
	return true
}

func isMetadata(path string) bool {
	return path == "addoninfo.txt" || path == "addonimage.jpg" || path == "addonimage.png"
}

func isToolGenerated(path string) bool {
	content, err := vpkmerge.ReadFile(path, "addoninfo.txt")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(content)), "l4d2 mod join")
}

func fingerprint(packages []vpkmerge.PackageInfo) string {
	hash := sha256.New()
	sorted := append([]vpkmerge.PackageInfo(nil), packages...)
	sort.Slice(sorted, func(i, j int) bool {
		return strings.ToLower(filepath.Base(sorted[i].Path)) < strings.ToLower(filepath.Base(sorted[j].Path))
	})
	for _, pkg := range sorted {
		fmt.Fprintf(hash, "P:%s:%s\n", strings.ToLower(filepath.Base(pkg.Path)), pkg.Digest)
		files := append([]vpkmerge.FileInfo(nil), pkg.Files...)
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		for _, file := range files {
			fmt.Fprintf(hash, "F:%s:%08x:%d\n", file.Path, file.CRC, file.Length)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func Fingerprint(directory string) (string, error) {
	result, err := Scan(directory)
	if err != nil {
		return "", err
	}
	return result.Fingerprint, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
