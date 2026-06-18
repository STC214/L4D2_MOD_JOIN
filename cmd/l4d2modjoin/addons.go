package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"l4d2-mod-join/internal/vpkmerge"
)

var addonLine = regexp.MustCompile(`^(\s*)"([^"]+)"(\s+)"([01])"(.*)$`)

const deploymentManifestName = ".l4d2modjoin-deployment.json"

var legacyManagedOutputs = []string{
	"01_UI_HUD.vpk", "02_Survivors.vpk", "03_Infected.vpk", "04_Weapons.vpk",
	"05_Environment.vpk", "06_Effects.vpk", "07_Audio.vpk", "08_Gameplay.vpk",
	"09_Sprays.vpk", "10_TUMTaRA.vpk", "10_Maps.vpk", "11_AlwaysToast_LDR.vpk",
	"11_Misc.vpk", "12_Training_Map.vpk",
}

func deployAndDisable(manifest buildManifest, outputDir, addonsDir string) (string, []string, error) {
	if addonsDir == "" {
		return "", nil, fmt.Errorf("未找到 Left 4 Dead 2 addons 目录")
	}
	if err := os.MkdirAll(addonsDir, 0755); err != nil {
		return "", nil, err
	}

	stamp := time.Now().Format("20060102-150405.000000000")
	stageRoot := filepath.Join(filepath.Dir(addonsDir), "l4d2modjoin_staging")
	if err := os.MkdirAll(stageRoot, 0755); err != nil {
		return "", nil, err
	}
	stageDir, err := os.MkdirTemp(stageRoot, "stage-*")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(stageDir)

	// Phase 1: stage and verify every output before touching the live addons.
	for _, file := range manifest.Files {
		src := filepath.Join(outputDir, file.Name)
		dst := filepath.Join(stageDir, file.Name)
		if err := copyFile(src, dst); err != nil {
			return "", nil, fmt.Errorf("暂存 %s: %w", file.Name, err)
		}
		digest, err := hashFile(dst)
		if err != nil || digest != file.SHA256 {
			return "", nil, fmt.Errorf("暂存文件校验失败: %s", file.Name)
		}
	}

	addonList := filepath.Join(filepath.Dir(addonsDir), "addonlist.txt")
	oldAddonList, err := os.ReadFile(addonList)
	hadAddonList := err == nil
	if err != nil && !os.IsNotExist(err) {
		return "", nil, err
	}
	backup := addonList + ".l4d2modjoin." + stamp + ".bak"
	if len(oldAddonList) > 0 {
		if err := writeFileAtomic(backup, oldAddonList, 0644); err != nil {
			return "", nil, err
		}
	}

	previous := readDeploymentManifest(filepath.Join(addonsDir, deploymentManifestName))
	managed := map[string]bool{}
	for _, name := range legacyManagedOutputs {
		live := findCaseInsensitive(addonsDir, strings.ToLower(name))
		if live != "" && isToolGeneratedVPK(live) {
			managed[strings.ToLower(name)] = true
		}
	}
	for _, file := range previous.Files {
		managed[strings.ToLower(file.Name)] = true
	}
	for _, file := range manifest.Files {
		managed[strings.ToLower(file.Name)] = true
	}
	localDuplicates, err := findLocalDuplicateMods(addonsDir, manifest, managed)
	if err != nil {
		return "", nil, err
	}
	manifest.Packages = appendUnique(manifest.Packages, localDuplicates...)
	newAddonList := updateAddonList(oldAddonList, manifest.Packages, manifest.Files, managed)

	// Phase 2: move all affected live files into a rollback directory, then
	// publish staged files. Nothing is deleted.
	// Keep backups outside addons so the game cannot discover or mount them.
	backupRoot := filepath.Join(filepath.Dir(addonsDir), "l4d2modjoin_backup")
	if err := os.MkdirAll(backupRoot, 0755); err != nil {
		return "", nil, err
	}
	rollbackDir, err := os.MkdirTemp(backupRoot, stamp+"-*")
	if err != nil {
		return "", nil, err
	}
	var published []string
	var moved []string
	rollback := func() error {
		var failures []string
		for _, name := range published {
			if err := os.Remove(filepath.Join(addonsDir, name)); err != nil && !os.IsNotExist(err) {
				failures = append(failures, err.Error())
			}
		}
		for _, name := range moved {
			if err := os.Rename(filepath.Join(rollbackDir, name), filepath.Join(addonsDir, name)); err != nil {
				failures = append(failures, err.Error())
			}
		}
		if hadAddonList {
			if err := writeFileAtomic(addonList, oldAddonList, 0644); err != nil {
				failures = append(failures, err.Error())
			}
		} else {
			if err := os.Remove(addonList); err != nil && !os.IsNotExist(err) {
				failures = append(failures, err.Error())
			}
		}
		if len(failures) > 0 {
			return fmt.Errorf("%s", strings.Join(failures, "; "))
		}
		return nil
	}
	for name := range managed {
		live := findCaseInsensitive(addonsDir, name)
		if live == "" {
			continue
		}
		base := filepath.Base(live)
		if err := os.Rename(live, filepath.Join(rollbackDir, base)); err != nil {
			rollbackErr := rollback()
			if rollbackErr != nil {
				return "", nil, fmt.Errorf("备份旧合并包 %s: %w；回滚失败: %v", base, err, rollbackErr)
			}
			return "", nil, fmt.Errorf("备份旧合并包 %s: %w", base, err)
		}
		moved = append(moved, base)
	}
	for _, file := range manifest.Files {
		if err := os.Rename(filepath.Join(stageDir, file.Name), filepath.Join(addonsDir, file.Name)); err != nil {
			rollbackErr := rollback()
			if rollbackErr != nil {
				return "", nil, fmt.Errorf("发布 %s: %w；回滚失败: %v", file.Name, err, rollbackErr)
			}
			return "", nil, fmt.Errorf("发布 %s: %w", file.Name, err)
		}
		published = append(published, file.Name)
	}
	if err := writeFileAtomic(addonList, newAddonList, 0644); err != nil {
		rollbackErr := rollback()
		if rollbackErr != nil {
			return "", nil, fmt.Errorf("写入 addonlist: %w；回滚失败: %v", err, rollbackErr)
		}
		return "", nil, err
	}
	if err := writeJSONAtomic(filepath.Join(addonsDir, deploymentManifestName), manifest); err != nil {
		rollbackErr := rollback()
		if rollbackErr != nil {
			return "", nil, fmt.Errorf("写入部署清单: %w；回滚失败: %v", err, rollbackErr)
		}
		return "", nil, err
	}

	// Remove empty backup folders only. Replaced/stale generated packages are
	// intentionally retained as rollback material.
	if entries, _ := os.ReadDir(rollbackDir); len(entries) == 0 {
		_ = os.Remove(rollbackDir)
	}
	return backup, localDuplicates, nil
}

func updateAddonList(data []byte, packages []string, files []builtFile, managed map[string]bool) []byte {
	originals := map[string]bool{}
	for _, name := range packages {
		originals[strings.ToLower(name)] = true
		originals[strings.ToLower(`workshop\`+name)] = true
	}
	current := map[string]bool{}
	for _, file := range files {
		current[strings.ToLower(file.Name)] = true
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(data) == 0 {
		lines = []string{`"AddonList"`, "{", "}"}
	}
	seenCurrent := map[string]bool{}
	for index, line := range lines {
		match := addonLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name := strings.ToLower(strings.ReplaceAll(match[2], "/", `\`))
		base := strings.ToLower(filepath.Base(name))
		value := match[4]
		if originals[name] || managed[base] {
			value = "0"
		}
		if current[base] {
			value = "1"
			seenCurrent[base] = true
		}
		lines[index] = match[1] + `"` + match[2] + `"` + match[3] + `"` + value + `"` + match[5]
	}
	insertAt := len(lines)
	for index := len(lines) - 1; index >= 0; index-- {
		if strings.TrimSpace(lines[index]) == "}" {
			insertAt = index
			break
		}
	}
	var additions []string
	names := make([]string, 0, len(current))
	for name := range current {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, lowerName := range names {
		if seenCurrent[lowerName] {
			continue
		}
		actual := lowerName
		for _, file := range files {
			if strings.EqualFold(file.Name, lowerName) {
				actual = file.Name
				break
			}
		}
		additions = append(additions, "\t"+`"`+actual+`"`+"\t\t"+`"1"`)
	}
	lines = append(lines[:insertAt], append(additions, lines[insertAt:]...)...)
	return []byte(strings.Join(lines, "\r\n"))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func restoreLatest(addonsDir string) (string, error) {
	addonList := filepath.Join(filepath.Dir(addonsDir), "addonlist.txt")
	matches, err := filepath.Glob(addonList + ".l4d2modjoin.*.bak")
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("没有找到可恢复的 addonlist 备份")
	}
	sort.Strings(matches)
	// The oldest backup is the baseline from before this tool first changed
	// addon states. Restoring it avoids re-enabling an intermediate merge.
	baseline := matches[0]
	data, err := os.ReadFile(baseline)
	if err != nil {
		return "", err
	}
	deploymentPath := filepath.Join(addonsDir, deploymentManifestName)
	deployment := readDeploymentManifest(deploymentPath)
	// The oldest backup is the exact pre-deployment baseline. Preserve disabled
	// source/local MOD states instead of forcing every package back to enabled.
	restoreRoot := filepath.Join(filepath.Dir(addonsDir), "l4d2modjoin_backup")
	if err := os.MkdirAll(restoreRoot, 0755); err != nil {
		return "", err
	}
	restoreDir, err := os.MkdirTemp(restoreRoot, "restore-*")
	if err != nil {
		return "", err
	}
	var moved []string
	rollback := func() error {
		var failures []string
		for _, name := range moved {
			if err := os.Rename(filepath.Join(restoreDir, name), filepath.Join(addonsDir, name)); err != nil {
				failures = append(failures, err.Error())
			}
		}
		if len(failures) > 0 {
			return fmt.Errorf("%s", strings.Join(failures, "; "))
		}
		return nil
	}
	for _, file := range deployment.Files {
		live := findCaseInsensitive(addonsDir, strings.ToLower(file.Name))
		if live == "" {
			continue
		}
		name := filepath.Base(live)
		if err := os.Rename(live, filepath.Join(restoreDir, name)); err != nil {
			rollbackErr := rollback()
			if rollbackErr != nil {
				return "", fmt.Errorf("移出合并包 %s: %w；回滚失败: %v", name, err, rollbackErr)
			}
			return "", err
		}
		moved = append(moved, name)
	}
	if err := writeFileAtomic(addonList, data, 0644); err != nil {
		rollbackErr := rollback()
		if rollbackErr != nil {
			return "", fmt.Errorf("恢复 addonlist: %w；回滚失败: %v", err, rollbackErr)
		}
		return "", err
	}
	if err := os.Remove(deploymentPath); err != nil && !os.IsNotExist(err) {
		// The restored addonlist already disables current packages, and the
		// packages have been moved out. Keep this as a reported cleanup error.
		return "", fmt.Errorf("状态已恢复，但清理部署清单失败: %w", err)
	}
	if entries, _ := os.ReadDir(restoreDir); len(entries) == 0 {
		_ = os.Remove(restoreDir)
	}
	return baseline, nil
}

func readDeploymentManifest(path string) buildManifest {
	data, err := os.ReadFile(path)
	if err != nil {
		return buildManifest{}
	}
	var manifest buildManifest
	if json.Unmarshal(data, &manifest) != nil {
		return buildManifest{}
	}
	return manifest
}

func findCaseInsensitive(directory, lowerName string) string {
	entries, _ := os.ReadDir(directory)
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(entry.Name(), lowerName) {
			return filepath.Join(directory, entry.Name())
		}
	}
	return ""
}

func isToolGeneratedVPK(path string) bool {
	content, err := vpkmerge.ReadFile(path, "addoninfo.txt")
	if err != nil {
		return false
	}
	text := strings.ToLower(string(content))
	return strings.Contains(text, `addonauthor "l4d2 mod join"`) ||
		strings.Contains(text, `addonauthor	"l4d2 mod join"`)
}

func findLocalDuplicateMods(addonsDir string, manifest buildManifest, managed map[string]bool) ([]string, error) {
	digests := map[string]bool{}
	signatures := map[string]bool{}
	for _, source := range manifest.SourcePackages {
		if source.Digest != "" {
			digests[source.Digest] = true
		}
		if source.RuntimeSignature != "" {
			signatures[source.RuntimeSignature] = true
		}
	}
	paths, err := filepath.Glob(filepath.Join(addonsDir, "*.vpk"))
	if err != nil {
		return nil, err
	}
	var duplicates []string
	for _, path := range paths {
		name := filepath.Base(path)
		if managed[strings.ToLower(name)] || isToolGeneratedVPK(path) {
			continue
		}
		info, inspectErr := vpkmerge.Inspect(path)
		if inspectErr != nil {
			// An unrelated unreadable local VPK must not block deployment.
			continue
		}
		if digests[info.Digest] || signatures[info.RuntimeSignature] {
			duplicates = append(duplicates, name)
		}
	}
	sort.Strings(duplicates)
	return duplicates, nil
}

func appendUnique(values []string, additions ...string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		seen[strings.ToLower(value)] = true
	}
	for _, value := range additions {
		if seen[strings.ToLower(value)] {
			continue
		}
		values = append(values, value)
		seen[strings.ToLower(value)] = true
	}
	return values
}

func detectAddonsDir() string {
	candidates := []string{
		`C:\Program Files (x86)\Steam\steamapps\common\Left 4 Dead 2\left4dead2\addons`,
		`C:\Program Files\Steam\steamapps\common\Left 4 Dead 2\left4dead2\addons`,
	}
	for drive := 'C'; drive <= 'Z'; drive++ {
		candidates = append(candidates,
			fmt.Sprintf(`%c:\SteamLibrary\steamapps\common\Left 4 Dead 2\left4dead2\addons`, drive),
			fmt.Sprintf(`%c:\Program Files (x86)\Steam\steamapps\common\Left 4 Dead 2\left4dead2\addons`, drive))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}
