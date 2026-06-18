package main

import (
	"crypto/sha256"
	"encoding/hex"
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

type operationProgress func(current, total int64, text string)

var addonLine = regexp.MustCompile(`^(\s*)"([^"]+)"(\s+)"([01])"(.*)$`)

const deploymentManifestName = ".l4d2modjoin-deployment.json"

type deploymentRegistry struct {
	Version     int             `json:"version"`
	Deployments []buildManifest `json:"deployments"`
}

var legacyManagedOutputs = []string{
	"01_UI_HUD.vpk", "02_Survivors.vpk", "03_Infected.vpk", "04_Weapons.vpk",
	"05_Environment.vpk", "06_Effects.vpk", "07_Audio.vpk", "08_Gameplay.vpk",
	"09_Sprays.vpk", "10_TUMTaRA.vpk", "10_Maps.vpk", "11_AlwaysToast_LDR.vpk",
	"11_Misc.vpk", "12_Training_Map.vpk",
}

func deployAndDisable(manifest buildManifest, outputDir, addonsDir, stateDir string, progress operationProgress) (string, []string, error) {
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

	var totalBytes int64
	for _, file := range manifest.Files {
		totalBytes += file.Size
	}
	byteWorkTotal := totalBytes * 2
	var byteWorkDone int64
	lastBytePercent := int64(-1)
	reportByteProgress := func(done int64) {
		percent := deploymentBytePercent(done, byteWorkTotal)
		if percent == lastBytePercent {
			return
		}
		lastBytePercent = percent
		reportProgress(progress, percent, 100, "")
	}
	reportProgress(progress, 0, 100, "部署 · 正在暂存并校验合并包……")

	// Phase 1: stage and verify every output before touching the live addons.
	for index, file := range manifest.Files {
		src := filepath.Join(outputDir, file.Name)
		dst := filepath.Join(stageDir, file.Name)
		reportProgress(progress, deploymentBytePercent(byteWorkDone, byteWorkTotal), 100,
			fmt.Sprintf("部署 · 暂存 [%d/%d] %s", index+1, len(manifest.Files), file.Name))
		if err := copyFileProgress(src, dst, func(done int64) {
			reportByteProgress(byteWorkDone + done)
		}); err != nil {
			return "", nil, fmt.Errorf("暂存 %s: %w", file.Name, err)
		}
		byteWorkDone += file.Size
		reportProgress(progress, deploymentBytePercent(byteWorkDone, byteWorkTotal), 100,
			fmt.Sprintf("部署 · 校验 [%d/%d] %s", index+1, len(manifest.Files), file.Name))
		digest, err := hashFileProgress(dst, func(done int64) {
			reportByteProgress(byteWorkDone + done)
		})
		if err != nil || digest != file.SHA256 {
			return "", nil, fmt.Errorf("暂存文件校验失败: %s", file.Name)
		}
		byteWorkDone += file.Size
	}

	reportProgress(progress, 82, 100, "部署 · 正在备份 addonlist……")
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

	legacyDeploymentPath := filepath.Join(addonsDir, deploymentManifestName)
	registry, err := loadDeploymentRegistry(stateDir)
	if err != nil {
		return "", nil, err
	}
	previous, _ := registryDeployment(registry, addonsDir)
	if legacy, legacyErr := readLegacyDeploymentManifest(legacyDeploymentPath, addonsDir); legacyErr != nil {
		return "", nil, legacyErr
	} else if len(previous.Files) == 0 && len(legacy.Files) > 0 {
		previous = legacy
	}
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
	reportProgress(progress, 86, 100, "部署 · 正在识别 addons 中的重复非订阅 MOD……")
	localDuplicates, err := findLocalDuplicateMods(addonsDir, manifest, managed, func(current, total int64, text string) {
		position := int64(86)
		if total > 0 {
			position += current * 6 / total
		}
		reportProgress(progress, position, 100, text)
	})
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
	reportProgress(progress, 92, 100, "部署 · 正在归档旧合并包……")
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
	for index, file := range manifest.Files {
		reportProgress(progress, int64(94+(index*4)/maxInt(len(manifest.Files), 1)), 100,
			fmt.Sprintf("部署 · 发布 [%d/%d] %s", index+1, len(manifest.Files), file.Name))
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
	manifest.DeployedAddons = cleanPath(addonsDir)
	setRegistryDeployment(&registry, manifest)
	if err := writeDeploymentRegistry(stateDir, registry); err != nil {
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
	if err := os.Remove(legacyDeploymentPath); err != nil && !os.IsNotExist(err) {
		reportProgress(progress, 100, 100, "部署已完成，但旧版 addons 部署清单未能删除："+err.Error())
	}
	reportProgress(progress, 100, 100, "部署 · 已完成")
	return backup, localDuplicates, nil
}

func deploymentBytePercent(done, total int64) int64 {
	if total <= 0 {
		return 80
	}
	return done * 80 / total
}

func reportProgress(progress operationProgress, current, total int64, text string) {
	if progress != nil {
		progress(current, total, text)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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
	return copyFileProgress(src, dst, nil)
}

func copyFileProgress(src, dst string, progress func(int64)) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	var copied int64
	writer := io.Writer(out)
	if progress != nil {
		writer = writerFunc(func(data []byte) (int, error) {
			n, writeErr := out.Write(data)
			copied += int64(n)
			progress(copied)
			return n, writeErr
		})
	}
	_, copyErr := io.Copy(writer, in)
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

type writerFunc func([]byte) (int, error)

func (write writerFunc) Write(data []byte) (int, error) {
	return write(data)
}

func hashFileProgress(path string, progress func(int64)) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	var read int64
	writer := writerFunc(func(data []byte) (int, error) {
		n, writeErr := hash.Write(data)
		read += int64(n)
		if progress != nil {
			progress(read)
		}
		return n, writeErr
	})
	if _, err := io.Copy(writer, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func restoreLatest(addonsDir, stateDir string, progress operationProgress) (string, error) {
	reportProgress(progress, 0, 100, "还原 · 正在查找首次部署备份……")
	addonList := filepath.Join(filepath.Dir(addonsDir), "addonlist.txt")
	currentAddonList, currentErr := os.ReadFile(addonList)
	hadCurrentAddonList := currentErr == nil
	if currentErr != nil && !os.IsNotExist(currentErr) {
		return "", currentErr
	}
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
	legacyDeploymentPath := filepath.Join(addonsDir, deploymentManifestName)
	registry, err := loadDeploymentRegistry(stateDir)
	if err != nil {
		return "", err
	}
	deployment, found := registryDeployment(registry, addonsDir)
	usedLegacy := false
	if !found {
		deployment, err = readLegacyDeploymentManifest(legacyDeploymentPath, addonsDir)
		if err != nil {
			return "", err
		}
		usedLegacy = len(deployment.Files) > 0
	}
	if len(deployment.Files) == 0 {
		return "", fmt.Errorf("没有找到当前 addons 目录的有效部署清单，已阻止还原以避免遗留合并包")
	}
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
		if hadCurrentAddonList {
			if err := writeFileAtomic(addonList, currentAddonList, 0644); err != nil {
				failures = append(failures, err.Error())
			}
		} else if err := os.Remove(addonList); err != nil && !os.IsNotExist(err) {
			failures = append(failures, err.Error())
		}
		if len(failures) > 0 {
			return fmt.Errorf("%s", strings.Join(failures, "; "))
		}
		return nil
	}
	reportProgress(progress, 15, 100, "还原 · 正在移出当前合并包……")
	for index, file := range deployment.Files {
		reportProgress(progress, int64(15+(index*65)/maxInt(len(deployment.Files), 1)), 100,
			fmt.Sprintf("还原 · 移出 [%d/%d] %s", index+1, len(deployment.Files), file.Name))
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
	reportProgress(progress, 82, 100, "还原 · 正在恢复 addonlist 基线……")
	if err := writeFileAtomic(addonList, data, 0644); err != nil {
		rollbackErr := rollback()
		if rollbackErr != nil {
			return "", fmt.Errorf("恢复 addonlist: %w；回滚失败: %v", err, rollbackErr)
		}
		return "", err
	}
	removeRegistryDeployment(&registry, addonsDir)
	if err := writeDeploymentRegistry(stateDir, registry); err != nil {
		rollbackErr := rollback()
		if rollbackErr != nil {
			return "", fmt.Errorf("更新部署记录失败: %w；回滚失败: %v", err, rollbackErr)
		}
		return "", fmt.Errorf("更新部署记录失败: %w", err)
	}
	if entries, _ := os.ReadDir(restoreDir); len(entries) == 0 {
		_ = os.Remove(restoreDir)
	}
	if usedLegacy {
		if err := os.Remove(legacyDeploymentPath); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("状态已恢复，但清理旧版部署清单失败: %w", err)
		}
	}
	reportProgress(progress, 100, 100, "还原 · 已完成")
	return baseline, nil
}

func loadDeploymentRegistry(stateDir string) (deploymentRegistry, error) {
	path := filepath.Join(stateDir, deploymentManifestName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return deploymentRegistry{Version: 1}, nil
	}
	if err != nil {
		return deploymentRegistry{}, fmt.Errorf("读取部署记录失败: %w", err)
	}
	var registry deploymentRegistry
	if json.Unmarshal(data, &registry) == nil && registry.Version > 0 && registry.Deployments != nil {
		for index := range registry.Deployments {
			if err := normalizeDeploymentManifest(&registry.Deployments[index], ""); err != nil {
				return deploymentRegistry{}, fmt.Errorf("部署记录损坏: %w", err)
			}
		}
		return registry, nil
	}
	var legacy buildManifest
	if err := json.Unmarshal(data, &legacy); err != nil {
		return deploymentRegistry{}, fmt.Errorf("部署记录格式错误: %w", err)
	}
	if err := normalizeDeploymentManifest(&legacy, ""); err != nil {
		return deploymentRegistry{}, fmt.Errorf("部署记录损坏: %w", err)
	}
	return deploymentRegistry{Version: 1, Deployments: []buildManifest{legacy}}, nil
}

func migrateDeploymentRegistry(stateDir string) (bool, error) {
	path := filepath.Join(stateDir, deploymentManifestName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var current deploymentRegistry
	if json.Unmarshal(data, &current) == nil && current.Version > 0 && current.Deployments != nil {
		return false, nil
	}
	registry, err := loadDeploymentRegistry(stateDir)
	if err != nil {
		return false, err
	}
	if err := writeDeploymentRegistry(stateDir, registry); err != nil {
		return false, err
	}
	return true, nil
}

func readLegacyDeploymentManifest(path, addonsDir string) (buildManifest, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return buildManifest{}, nil
	}
	if err != nil {
		return buildManifest{}, fmt.Errorf("读取旧版部署清单失败: %w", err)
	}
	var manifest buildManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return buildManifest{}, fmt.Errorf("旧版部署清单格式错误: %w", err)
	}
	if err := normalizeDeploymentManifest(&manifest, addonsDir); err != nil {
		return buildManifest{}, fmt.Errorf("旧版部署清单损坏: %w", err)
	}
	return manifest, nil
}

func normalizeDeploymentManifest(manifest *buildManifest, fallbackAddons string) error {
	if manifest.Version <= 0 || len(manifest.Files) == 0 {
		return fmt.Errorf("缺少版本或已部署文件列表")
	}
	if manifest.DeployedAddons == "" {
		if fallbackAddons != "" {
			manifest.DeployedAddons = cleanPath(fallbackAddons)
		} else if strings.EqualFold(filepath.Base(manifest.Source), "workshop") {
			manifest.DeployedAddons = cleanPath(filepath.Dir(manifest.Source))
		}
	}
	if manifest.DeployedAddons == "" {
		return fmt.Errorf("无法确定部署清单所属的 addons 目录")
	}
	manifest.DeployedAddons = cleanPath(manifest.DeployedAddons)
	for _, file := range manifest.Files {
		if file.Name == "" || filepath.Base(file.Name) != file.Name {
			return fmt.Errorf("包含无效部署文件名")
		}
	}
	return nil
}

func registryDeployment(registry deploymentRegistry, addonsDir string) (buildManifest, bool) {
	wanted := cleanPath(addonsDir)
	for _, deployment := range registry.Deployments {
		if cleanPath(deployment.DeployedAddons) == wanted {
			return deployment, true
		}
	}
	return buildManifest{}, false
}

func setRegistryDeployment(registry *deploymentRegistry, manifest buildManifest) {
	registry.Version = 1
	for index := range registry.Deployments {
		if cleanPath(registry.Deployments[index].DeployedAddons) == cleanPath(manifest.DeployedAddons) {
			registry.Deployments[index] = manifest
			return
		}
	}
	registry.Deployments = append(registry.Deployments, manifest)
	sort.Slice(registry.Deployments, func(i, j int) bool {
		return registry.Deployments[i].DeployedAddons < registry.Deployments[j].DeployedAddons
	})
}

func removeRegistryDeployment(registry *deploymentRegistry, addonsDir string) {
	wanted := cleanPath(addonsDir)
	filtered := registry.Deployments[:0]
	for _, deployment := range registry.Deployments {
		if cleanPath(deployment.DeployedAddons) != wanted {
			filtered = append(filtered, deployment)
		}
	}
	registry.Deployments = filtered
}

func writeDeploymentRegistry(stateDir string, registry deploymentRegistry) error {
	registry.Version = 1
	return writeJSONAtomic(filepath.Join(stateDir, deploymentManifestName), registry)
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

func findLocalDuplicateMods(addonsDir string, manifest buildManifest, managed map[string]bool, callbacks ...operationProgress) ([]string, error) {
	var progress operationProgress
	if len(callbacks) > 0 {
		progress = callbacks[0]
	}
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
	reportProgress(progress, 0, int64(len(paths)), "")
	for index, path := range paths {
		name := filepath.Base(path)
		reportProgress(progress, int64(index), int64(len(paths)),
			fmt.Sprintf("部署 · 检查本地 VPK [%d/%d] %s", index+1, len(paths), name))
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
		reportProgress(progress, int64(index+1), int64(len(paths)), "")
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
