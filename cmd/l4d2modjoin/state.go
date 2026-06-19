package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"l4d2-mod-join/internal/modscan"
	"l4d2-mod-join/internal/vpkmerge"
)

const (
	stateDirectoryName = "data"
	scanReportName     = "mod-scan-report.json"
	conflictPolicyName = "mod-conflict-policy.json"
	buildManifestName  = "l4d2modjoin-build.json"
	settingsName       = "l4d2modjoin-settings.json"
)

func migrateRootStateFiles(rootDir, stateDir string) (int, error) {
	if cleanPath(rootDir) == cleanPath(stateDir) {
		return 0, nil
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return 0, fmt.Errorf("创建 JSON 状态目录失败: %w", err)
	}
	names := []string{
		scanReportName,
		conflictPolicyName,
		buildManifestName,
		deploymentManifestName,
		settingsName,
	}
	migrated := 0
	var failures []string
	for _, name := range names {
		source := filepath.Join(rootDir, name)
		destination := filepath.Join(stateDir, name)
		sourceData, err := os.ReadFile(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("读取 %s: %v", source, err))
			continue
		}
		if destinationData, destinationErr := os.ReadFile(destination); destinationErr == nil {
			if string(sourceData) != string(destinationData) {
				failures = append(failures, fmt.Sprintf("%s 与 data 中同名文件内容不同，已保留旧文件", source))
				continue
			}
			if err := os.Remove(source); err != nil {
				failures = append(failures, fmt.Sprintf("清理旧文件 %s: %v", source, err))
				continue
			}
			migrated++
			continue
		} else if !os.IsNotExist(destinationErr) {
			failures = append(failures, fmt.Sprintf("读取 %s: %v", destination, destinationErr))
			continue
		}
		if err := os.Rename(source, destination); err != nil {
			failures = append(failures, fmt.Sprintf("迁移 %s: %v", source, err))
			continue
		}
		migrated++
	}
	if len(failures) > 0 {
		return migrated, fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return migrated, nil
}

type appSettings struct {
	Version int    `json:"version"`
	Source  string `json:"source"`
	Output  string `json:"output"`
	Addons  string `json:"addons"`
}

type conflictChoice struct {
	Path      string   `json:"path"`
	Packages  []string `json:"packages"`
	Selected  string   `json:"selected"`
	SafeMerge bool     `json:"safe_merge"`
}

type conflictPolicy struct {
	Fingerprint string           `json:"fingerprint"`
	Conflicts   []conflictChoice `json:"conflicts"`
}

type builtFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type sourcePackage struct {
	Name             string `json:"name"`
	Digest           string `json:"digest"`
	RuntimeSignature string `json:"runtime_signature"`
}

type buildManifest struct {
	Version        int             `json:"version"`
	BuiltAt        time.Time       `json:"built_at"`
	Source         string          `json:"source"`
	Fingerprint    string          `json:"fingerprint"`
	PolicySHA256   string          `json:"policy_sha256"`
	Files          []builtFile     `json:"files"`
	Packages       []string        `json:"packages"`
	SourcePackages []sourcePackage `json:"source_packages"`
	DeployedAddons string          `json:"deployed_addons,omitempty"`
}

func loadAppSettings(stateDir string) (appSettings, error) {
	path := filepath.Join(stateDir, settingsName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return appSettings{Version: 1}, nil
	}
	if err != nil {
		return appSettings{}, fmt.Errorf("读取目录设置失败: %w", err)
	}
	var settings appSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return appSettings{}, fmt.Errorf("目录设置格式错误: %w", err)
	}
	if settings.Version != 1 {
		return appSettings{}, fmt.Errorf("不支持的目录设置版本: %d", settings.Version)
	}
	settings.Source = strings.TrimSpace(settings.Source)
	settings.Output = strings.TrimSpace(settings.Output)
	settings.Addons = strings.TrimSpace(settings.Addons)
	return settings, nil
}

func saveAppSettings(stateDir string, settings appSettings) error {
	settings.Version = 1
	settings.Source = strings.TrimSpace(settings.Source)
	settings.Output = strings.TrimSpace(settings.Output)
	settings.Addons = strings.TrimSpace(settings.Addons)
	return writeJSONAtomic(filepath.Join(stateDir, settingsName), settings)
}

func writeConflictPolicy(output string, result modscan.Result) error {
	path := filepath.Join(output, conflictPolicyName)
	existing := map[string]string{}
	if data, err := os.ReadFile(path); err == nil {
		var previous conflictPolicy
		if json.Unmarshal(data, &previous) == nil && previous.Fingerprint == result.Fingerprint {
			for _, conflict := range previous.Conflicts {
				existing[conflict.Path] = conflict.Selected
			}
		}
	}
	policy := conflictPolicy{Fingerprint: result.Fingerprint}
	for _, conflict := range result.Conflicts {
		if conflict.Identical {
			continue
		}
		selected := existing[conflict.Path]
		if conflict.AutoWinner != "" {
			selected = conflict.AutoWinner
		}
		if !contains(conflict.Packages, selected) {
			selected = ""
		}
		policy.Conflicts = append(policy.Conflicts, conflictChoice{
			Path: conflict.Path, Packages: conflict.Packages,
			Selected: selected, SafeMerge: conflict.SafeMerge,
		})
	}
	return writeJSONAtomic(path, policy)
}

func unresolvedConflictGroups(output string, result modscan.Result) ([]modscan.ConflictGroup, map[string]string, error) {
	path := filepath.Join(output, conflictPolicyName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var policy conflictPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return nil, nil, err
	}
	if policy.Fingerprint != result.Fingerprint {
		return nil, nil, fmt.Errorf("冲突策略已过期，请重新扫描")
	}
	selectedByPath := map[string]string{}
	for _, choice := range policy.Conflicts {
		if contains(choice.Packages, choice.Selected) {
			selectedByPath[choice.Path] = choice.Selected
		}
	}
	var unresolved []modscan.ConflictGroup
	for _, group := range result.ConflictGroups {
		if group.AutoResolved {
			continue
		}
		resolved := true
		for _, path := range group.Paths {
			if !contains(group.Packages, selectedByPath[path]) {
				resolved = false
				break
			}
		}
		if !resolved {
			unresolved = append(unresolved, group)
		}
	}
	return unresolved, selectedByPath, nil
}

func saveConflictGroupSelections(output string, result modscan.Result, selections map[string]string) error {
	path := filepath.Join(output, conflictPolicyName)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var policy conflictPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return err
	}
	if policy.Fingerprint != result.Fingerprint {
		return fmt.Errorf("冲突策略已过期，请重新扫描")
	}
	for index := range policy.Conflicts {
		if selected := selections[policy.Conflicts[index].Path]; contains(policy.Conflicts[index].Packages, selected) {
			policy.Conflicts[index].Selected = selected
		}
	}
	return writeJSONAtomic(path, policy)
}

func loadConflictSelections(output, fingerprint string) (map[string]string, error) {
	path := filepath.Join(output, conflictPolicyName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("请先编辑冲突策略文件 %s", path)
	}
	var policy conflictPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return nil, fmt.Errorf("冲突策略文件格式错误: %w", err)
	}
	if policy.Fingerprint != fingerprint {
		return nil, fmt.Errorf("冲突策略已过期，请重新扫描")
	}
	selections := map[string]string{}
	var unresolved []string
	for _, conflict := range policy.Conflicts {
		if conflict.SafeMerge {
			continue
		}
		if !contains(conflict.Packages, conflict.Selected) {
			unresolved = append(unresolved, conflict.Path)
			continue
		}
		selections[conflict.Path] = conflict.Selected
	}
	if len(unresolved) > 0 {
		return nil, fmt.Errorf("还有 %d 个冲突未选择；请在 %s 中填写 selected", len(unresolved), path)
	}
	return selections, nil
}

func createBuildManifest(plan vpkmerge.Plan, scanResult modscan.Result, policyDigest, stateDir string) (buildManifest, error) {
	manifest := buildManifest{
		Version: 2, BuiltAt: time.Now(), Source: cleanPath(plan.Input),
		Fingerprint: scanResult.Fingerprint, PolicySHA256: policyDigest,
	}
	packageSet := map[string]bool{}
	owners := map[string]string{}
	for _, group := range plan.Groups {
		path := filepath.Join(plan.Output, group.Output)
		info, verifyErr := vpkmerge.Verify(path)
		if verifyErr != nil {
			return buildManifest{}, fmt.Errorf("%s 校验失败: %w", group.Output, verifyErr)
		}
		for _, file := range info.Files {
			if file.Path == "addoninfo.txt" {
				continue
			}
			if previous := owners[file.Path]; previous != "" {
				return buildManifest{}, fmt.Errorf("输出包之间仍有重复路径 %s (%s, %s)", file.Path, previous, group.Output)
			}
			owners[file.Path] = group.Output
		}
		stat, err := os.Stat(path)
		if err != nil {
			return buildManifest{}, err
		}
		digest, err := hashFile(path)
		if err != nil {
			return buildManifest{}, err
		}
		manifest.Files = append(manifest.Files, builtFile{Name: group.Output, Size: stat.Size(), SHA256: digest})
		for _, name := range group.Packages {
			packageSet[name] = true
		}
	}
	for name := range packageSet {
		manifest.Packages = append(manifest.Packages, name)
	}
	sort.Strings(manifest.Packages)
	for _, info := range scanResult.Packages {
		manifest.SourcePackages = append(manifest.SourcePackages, sourcePackage{
			Name: filepath.Base(info.Path), Digest: info.Digest, RuntimeSignature: info.RuntimeSignature,
		})
	}
	sort.Slice(manifest.SourcePackages, func(i, j int) bool {
		return strings.ToLower(manifest.SourcePackages[i].Name) < strings.ToLower(manifest.SourcePackages[j].Name)
	})
	if err := archiveStaleBuildOutputs(plan); err != nil {
		return buildManifest{}, err
	}
	if err := writeJSONAtomic(filepath.Join(stateDir, buildManifestName), manifest); err != nil {
		return buildManifest{}, err
	}
	return manifest, nil
}

func archiveStaleBuildOutputs(plan vpkmerge.Plan) error {
	current := map[string]bool{}
	for _, group := range plan.Groups {
		current[strings.ToLower(group.Output)] = true
	}
	paths, err := filepath.Glob(filepath.Join(plan.Output, "*.vpk"))
	if err != nil {
		return err
	}
	var stale []string
	for _, path := range paths {
		if current[strings.ToLower(filepath.Base(path))] || !isToolGeneratedVPK(path) {
			continue
		}
		stale = append(stale, path)
	}
	if len(stale) == 0 {
		return nil
	}
	archiveRoot := filepath.Join(plan.Output, "l4d2modjoin_old")
	if err := os.MkdirAll(archiveRoot, 0755); err != nil {
		return err
	}
	archiveDir, err := os.MkdirTemp(archiveRoot, time.Now().Format("20060102-150405")+"-*")
	if err != nil {
		return err
	}
	var moved []string
	for _, path := range stale {
		name := filepath.Base(path)
		if err := os.Rename(path, filepath.Join(archiveDir, name)); err != nil {
			for _, previous := range moved {
				_ = os.Rename(filepath.Join(archiveDir, previous), filepath.Join(plan.Output, previous))
			}
			return fmt.Errorf("归档旧输出 %s: %w", name, err)
		}
		moved = append(moved, name)
	}
	return nil
}

func validateBuild(output, stateDir string, result *modscan.Result) (buildManifest, error) {
	return validateBuildProgress(output, stateDir, result, nil)
}

func validateBuildProgress(output, stateDir string, result *modscan.Result, progress operationProgress) (buildManifest, error) {
	if result == nil {
		return buildManifest{}, fmt.Errorf("请先完成智能扫描")
	}
	data, err := os.ReadFile(filepath.Join(stateDir, buildManifestName))
	if err != nil {
		return buildManifest{}, fmt.Errorf("没有找到成功构建清单，请先合并")
	}
	var manifest buildManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return buildManifest{}, err
	}
	if manifest.Fingerprint != result.Fingerprint || cleanPath(manifest.Source) != cleanPath(result.Directory) {
		return buildManifest{}, fmt.Errorf("构建产物与当前扫描结果不一致，请重新合并")
	}
	policyDigest, err := hashFile(filepath.Join(stateDir, conflictPolicyName))
	if err != nil || policyDigest != manifest.PolicySHA256 {
		return buildManifest{}, fmt.Errorf("冲突策略在构建后发生变化，请重新合并")
	}
	current, err := modscan.Fingerprint(result.Directory)
	if err != nil || current != result.Fingerprint {
		return buildManifest{}, fmt.Errorf("源 MOD 在扫描后发生变化，请重新扫描并合并")
	}
	var totalBytes int64
	for _, file := range manifest.Files {
		totalBytes += file.Size
	}
	var completed int64
	lastPercent := int64(-1)
	reportBytes := func(done int64) {
		percent := int64(100)
		if totalBytes > 0 {
			percent = done * 100 / totalBytes
		}
		if percent == lastPercent {
			return
		}
		lastPercent = percent
		reportProgress(progress, done, totalBytes, "")
	}
	reportProgress(progress, 0, totalBytes, "部署 · 正在校验构建产物……")
	for index, file := range manifest.Files {
		path := filepath.Join(output, file.Name)
		stat, statErr := os.Stat(path)
		if statErr != nil || stat.Size() != file.Size {
			return buildManifest{}, fmt.Errorf("输出文件缺失或大小改变: %s", file.Name)
		}
		reportProgress(progress, completed, totalBytes,
			fmt.Sprintf("部署 · 预检 [%d/%d] %s", index+1, len(manifest.Files), file.Name))
		digest, hashErr := hashFileProgress(path, func(done int64) {
			reportBytes(completed + done)
		})
		if hashErr != nil || digest != file.SHA256 {
			return buildManifest{}, fmt.Errorf("输出文件校验失败: %s", file.Name)
		}
		completed += file.Size
	}
	reportProgress(progress, totalBytes, totalBytes, "部署 · 构建产物校验通过")
	return manifest, nil
}

func removeLegacyOutputJSON(output, stateDir string) error {
	if cleanPath(output) == cleanPath(stateDir) {
		return nil
	}
	var failures []string
	for _, name := range []string{scanReportName, conflictPolicyName, buildManifestName} {
		legacyPath := filepath.Join(output, name)
		statePath := filepath.Join(stateDir, name)
		matches, err := matchingToolJSON(legacyPath, statePath, name)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if !matches {
			continue
		}
		if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
			failures = append(failures, fmt.Sprintf("清理旧状态文件 %s: %v", legacyPath, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func matchingToolJSON(legacyPath, statePath, name string) (bool, error) {
	legacyData, err := os.ReadFile(legacyPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取旧状态文件 %s: %w", legacyPath, err)
	}
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		return false, nil
	}
	switch name {
	case scanReportName:
		var legacy, current modscan.Result
		if json.Unmarshal(legacyData, &legacy) != nil || json.Unmarshal(stateData, &current) != nil {
			return false, nil
		}
		return legacy.Fingerprint != "" && legacy.Fingerprint == current.Fingerprint, nil
	case conflictPolicyName:
		var legacy, current conflictPolicy
		if json.Unmarshal(legacyData, &legacy) != nil || json.Unmarshal(stateData, &current) != nil {
			return false, nil
		}
		return legacy.Fingerprint != "" && legacy.Fingerprint == current.Fingerprint, nil
	case buildManifestName:
		var legacy, current buildManifest
		if json.Unmarshal(legacyData, &legacy) != nil || json.Unmarshal(stateData, &current) != nil {
			return false, nil
		}
		return legacy.Version > 0 && legacy.Fingerprint != "" &&
			legacy.Fingerprint == current.Fingerprint && cleanPath(legacy.Source) == cleanPath(current.Source), nil
	}
	return false, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0644)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceStateFile(tmpName, path)
}

func cleanPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	return strings.ToLower(filepath.Clean(absolute))
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func pathWithin(path, parent string) bool {
	if path == "" || parent == "" {
		return false
	}
	child := cleanPath(path)
	root := cleanPath(parent)
	relative, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func subscriptionChanges(result modscan.Result, deployment buildManifest) (added, removed []string) {
	current := map[string]bool{}
	for _, info := range result.Packages {
		current[strings.ToLower(filepath.Base(info.Path))] = true
	}
	previous := map[string]bool{}
	for _, source := range deployment.SourcePackages {
		previous[strings.ToLower(source.Name)] = true
	}
	if len(previous) == 0 {
		for _, name := range deployment.Packages {
			previous[strings.ToLower(filepath.Base(name))] = true
		}
	}
	for _, info := range result.Packages {
		name := filepath.Base(info.Path)
		if !previous[strings.ToLower(name)] {
			added = append(added, name)
		}
	}
	for _, source := range deployment.SourcePackages {
		if !current[strings.ToLower(source.Name)] {
			removed = append(removed, source.Name)
		}
	}
	if len(deployment.SourcePackages) == 0 {
		for _, name := range deployment.Packages {
			base := filepath.Base(name)
			if !current[strings.ToLower(base)] {
				removed = append(removed, base)
			}
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}
