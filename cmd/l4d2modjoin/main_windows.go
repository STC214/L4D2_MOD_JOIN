//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"l4d2-mod-join/internal/modscan"
	"l4d2-mod-join/internal/vpkmerge"
)

const (
	wmCreate         = 0x0001
	wmDestroy        = 0x0002
	wmPaint          = 0x000F
	wmEraseBkgnd     = 0x0014
	wmClose          = 0x0010
	wmCommand        = 0x0111
	wmSetFont        = 0x0030
	wmSetIcon        = 0x0080
	wmSetRedraw      = 0x000B
	wmCtlColorEdit   = 0x0133
	wmCtlColorList   = 0x0134
	wmCtlColorStatic = 0x0138
	wmAppEvent       = 0x8001
	wsVisible        = 0x10000000
	wsChild          = 0x40000000
	wsTabStop        = 0x00010000
	wsBorder         = 0x00800000
	wsOverlapped     = 0x00CF0000
	wsClipChildren   = 0x02000000
	esAutoHScroll    = 0x0080
	bsPushButton     = 0
	lbsNoIntegral    = 0x0100
	lbAddString      = 0x0180
	lbInsertString   = 0x0181
	lbSetCurSel      = 0x0186
	lbGetTopIndex    = 0x018E
	lbSetTopIndex    = 0x0197
	pbmSetRange32    = 0x0406
	pbmSetPos        = 0x0402
	swShow           = 5
	colorWindow      = 5
	idScan           = 1001
	idMerge          = 1002
	idDeploy         = 1003
	idRestore        = 1004
	idBrowseSrc      = 1011
	idBrowseOut      = 1012
	idBrowseGame     = 1013
	idSourceEdit     = 1021
	enChange         = 0x0300
)

var (
	user32             = syscall.NewLazyDLL("user32.dll")
	gdi32              = syscall.NewLazyDLL("gdi32.dll")
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	comctl32           = syscall.NewLazyDLL("comctl32.dll")
	shell32            = syscall.NewLazyDLL("shell32.dll")
	ole32              = syscall.NewLazyDLL("ole32.dll")
	dwmapi             = syscall.NewLazyDLL("dwmapi.dll")
	procRegisterClass  = user32.NewProc("RegisterClassExW")
	procCreateWindow   = user32.NewProc("CreateWindowExW")
	procLoadImage      = user32.NewProc("LoadImageW")
	procDefWindowProc  = user32.NewProc("DefWindowProcW")
	procShowWindow     = user32.NewProc("ShowWindow")
	procUpdateWindow   = user32.NewProc("UpdateWindow")
	procGetMessage     = user32.NewProc("GetMessageW")
	procTranslate      = user32.NewProc("TranslateMessage")
	procDispatch       = user32.NewProc("DispatchMessageW")
	procPostQuit       = user32.NewProc("PostQuitMessage")
	procSendMessage    = user32.NewProc("SendMessageW")
	procPostMessage    = user32.NewProc("PostMessageW")
	procSetWindowText  = user32.NewProc("SetWindowTextW")
	procGetWindowText  = user32.NewProc("GetWindowTextW")
	procGetWindowLen   = user32.NewProc("GetWindowTextLengthW")
	procEnableWindow   = user32.NewProc("EnableWindow")
	procInvalidateRect = user32.NewProc("InvalidateRect")
	procGetClientRect  = user32.NewProc("GetClientRect")
	procBeginPaint     = user32.NewProc("BeginPaint")
	procEndPaint       = user32.NewProc("EndPaint")
	procFillRect       = user32.NewProc("FillRect")
	procSetTextColor   = gdi32.NewProc("SetTextColor")
	procSetBkColor     = gdi32.NewProc("SetBkColor")
	procSetBkMode      = gdi32.NewProc("SetBkMode")
	procTextOut        = gdi32.NewProc("TextOutW")
	procSelectObject   = gdi32.NewProc("SelectObject")
	procCreateBrush    = gdi32.NewProc("CreateSolidBrush")
	procCreateFont     = gdi32.NewProc("CreateFontW")
	procDeleteObject   = gdi32.NewProc("DeleteObject")
	procCreateCompatDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatBM = gdi32.NewProc("CreateCompatibleBitmap")
	procDeleteDC       = gdi32.NewProc("DeleteDC")
	procBitBlt         = gdi32.NewProc("BitBlt")
	procGetModule      = kernel32.NewProc("GetModuleHandleW")
	procInitControls   = comctl32.NewProc("InitCommonControls")
	procBrowseFolder   = shell32.NewProc("SHBrowseForFolderW")
	procGetPathPIDL    = shell32.NewProc("SHGetPathFromIDListW")
	procCoTaskMemFree  = ole32.NewProc("CoTaskMemFree")
	procCoInitializeEx = ole32.NewProc("CoInitializeEx")
	procDwmSetAttr     = dwmapi.NewProc("DwmSetWindowAttribute")
)

type point struct{ X, Y int32 }
type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}
type wndClassEx struct {
	Size, Style uint32
	WndProc     uintptr
	ClsExtra    int32
	WndExtra    int32
	Instance    uintptr
	Icon        uintptr
	Cursor      uintptr
	Background  uintptr
	MenuName    *uint16
	ClassName   *uint16
	IconSm      uintptr
}
type rect struct{ Left, Top, Right, Bottom int32 }
type paintStruct struct {
	Hdc       uintptr
	Erase     int32
	Paint     rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}
type browseInfo struct {
	Owner       uintptr
	Root        uintptr
	DisplayName *uint16
	Title       *uint16
	Flags       uint32
	Callback    uintptr
	LParam      uintptr
	Image       int32
}
type appEvent struct {
	Kind, Text     string
	Current, Total int64
	Data           any
}

type uiState struct {
	hwnd, source, output, addons, log, progress uintptr
	scan, merge, deploy, restore                uintptr
	font, titleFont                             uintptr
	bgBrush, fieldBrush                         uintptr
	stateDir                                    string
	scanResult                                  *modscan.Result
	busy                                        bool
}

var (
	ui          uiState
	eventID     uint64
	eventValues sync.Map
)

func utf16(value string) *uint16 { return syscall.StringToUTF16Ptr(value) }

func main() {
	runtime.LockOSThread()
	procCoInitializeEx.Call(0, 2)
	procInitControls.Call()
	instance, _, _ := procGetModule.Call(0)
	// rsrc stores the RT_GROUP_ICON resource under ID 2.
	iconLarge, _, _ := procLoadImage.Call(instance, 2, 1, 32, 32, 0)
	iconSmall, _, _ := procLoadImage.Call(instance, 2, 1, 16, 16, 0)
	registerConflictClass(instance, iconLarge, iconSmall)
	className := utf16("L4D2ModJoinWindow")
	wc := wndClassEx{
		Size: uint32(unsafe.Sizeof(wndClassEx{})), WndProc: syscall.NewCallback(windowProc),
		Instance: instance, Icon: iconLarge, IconSm: iconSmall,
		Background: colorWindow + 1, ClassName: className,
	}
	procRegisterClass.Call(uintptr(unsafe.Pointer(&wc)))
	hwnd, _, _ := procCreateWindow.Call(
		0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(utf16("L4D2 Mod Join V2"))),
		wsOverlapped|wsClipChildren, 180, 90, 980, 720, 0, 0, instance, 0,
	)
	if hwnd == 0 {
		return
	}
	enableDarkTitleBar(hwnd)
	procSendMessage.Call(hwnd, wmSetIcon, 1, iconLarge)
	procSendMessage.Call(hwnd, wmSetIcon, 0, iconSmall)
	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)
	var message msg
	for {
		result, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(result) <= 0 {
			break
		}
		procTranslate.Call(uintptr(unsafe.Pointer(&message)))
		procDispatch.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func enableDarkTitleBar(hwnd uintptr) {
	enabled := int32(1)
	// DWMWA_USE_IMMERSIVE_DARK_MODE is 20 on current Windows 10/11 and 19
	// on older Windows 10 builds.
	if result, _, _ := procDwmSetAttr.Call(hwnd, 20, uintptr(unsafe.Pointer(&enabled)), unsafe.Sizeof(enabled)); int32(result) != 0 {
		procDwmSetAttr.Call(hwnd, 19, uintptr(unsafe.Pointer(&enabled)), unsafe.Sizeof(enabled))
	}
}

func windowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmCreate:
		ui.hwnd = hwnd
		createUI(hwnd)
		return 0
	case wmCommand:
		id := int(wParam & 0xffff)
		code := int((wParam >> 16) & 0xffff)
		if id == idSourceEdit && code == enChange {
			ui.scanResult = nil
		}
		if id != 0 {
			handleCommand(id)
		}
		return 0
	case wmAppEvent:
		handleEvent(uint64(wParam))
		return 0
	case wmCtlColorStatic:
		procSetTextColor.Call(wParam, 0x00E8E4DF)
		procSetBkMode.Call(wParam, 1)
		return ui.bgBrush
	case wmCtlColorEdit, wmCtlColorList:
		procSetTextColor.Call(wParam, 0x00F0ECE8)
		procSetBkColor.Call(wParam, 0x00352E2A)
		return ui.fieldBrush
	case wmPaint:
		paintWindow(hwnd)
		return 0
	case wmEraseBkgnd:
		// WM_PAINT draws the complete background through a memory DC.
		return 1
	case wmClose:
		if ui.busy {
			logLine("任务仍在运行，请等待完成后关闭。")
			return 0
		}
	case wmDestroy:
		if ui.font != 0 {
			procDeleteObject.Call(ui.font)
		}
		if ui.titleFont != 0 {
			procDeleteObject.Call(ui.titleFont)
		}
		if ui.bgBrush != 0 {
			procDeleteObject.Call(ui.bgBrush)
		}
		if ui.fieldBrush != 0 {
			procDeleteObject.Call(ui.fieldBrush)
		}
		procPostQuit.Call(0)
		return 0
	}
	result, _, _ := procDefWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
	return result
}

func createUI(hwnd uintptr) {
	ui.bgBrush, _, _ = procCreateBrush.Call(0x00241E1B)
	ui.fieldBrush, _, _ = procCreateBrush.Call(0x00352E2A)
	cwd, _ := os.Getwd()
	base := cwd
	if executable, err := os.Executable(); err == nil {
		executableDir := filepath.Dir(executable)
		ui.stateDir = executableDir
		if info, statErr := os.Stat(filepath.Join(executableDir, "workshop")); statErr == nil && info.IsDir() {
			base = executableDir
		}
	}
	if ui.stateDir == "" {
		ui.stateDir = cwd
	}
	deploymentMigrated, deploymentMigrationErr := migrateDeploymentRegistry(ui.stateDir)
	source := filepath.Join(base, "workshop")
	output := filepath.Join(base, "merged")
	addons := detectAddonsDir()
	ui.font, _, _ = procCreateFont.Call(19, 0, 0, 0, 400, 0, 0, 0, 134, 0, 0, 5, 0, uintptr(unsafe.Pointer(utf16("Segoe UI"))))
	ui.titleFont, _, _ = procCreateFont.Call(34, 0, 0, 0, 600, 0, 0, 0, 134, 0, 0, 5, 0, uintptr(unsafe.Pointer(utf16("Segoe UI"))))

	makeLabel(hwnd, "MOD 来源目录", 42, 118, 160, 24)
	ui.source = makeControl(hwnd, "EDIT", source, wsChild|wsVisible|wsBorder|esAutoHScroll|wsTabStop, 42, 145, 790, 34, idSourceEdit)
	makeButton(hwnd, "浏览", 844, 145, 88, 34, idBrowseSrc)
	makeLabel(hwnd, "合并输出目录", 42, 193, 160, 24)
	ui.output = makeControl(hwnd, "EDIT", output, wsChild|wsVisible|wsBorder|esAutoHScroll|wsTabStop, 42, 220, 790, 34, 0)
	makeButton(hwnd, "浏览", 844, 220, 88, 34, idBrowseOut)
	makeLabel(hwnd, "游戏 Addons 目录", 42, 268, 180, 24)
	ui.addons = makeControl(hwnd, "EDIT", addons, wsChild|wsVisible|wsBorder|esAutoHScroll|wsTabStop, 42, 295, 790, 34, 0)
	makeButton(hwnd, "浏览", 844, 295, 88, 34, idBrowseGame)

	ui.scan = makeButton(hwnd, "智能扫描 MOD", 42, 355, 150, 44, idScan)
	ui.merge = makeButton(hwnd, "一键分类合并", 206, 355, 180, 44, idMerge)
	ui.deploy = makeButton(hwnd, "部署并禁用原 MOD", 400, 355, 220, 44, idDeploy)
	ui.restore = makeButton(hwnd, "一键还原官方MOD", 634, 355, 170, 44, idRestore)
	ui.progress = makeControl(hwnd, "msctls_progress32", "", wsChild|wsVisible, 42, 420, 890, 18, 0)
	procSendMessage.Call(ui.progress, pbmSetRange32, 0, 100)
	ui.log = makeControl(hwnd, "LISTBOX", "", wsChild|wsVisible|wsBorder|wsVScroll|lbsNoIntegral, 42, 462, 890, 190, 0)
	logLine("就绪。耗时操作将在后台执行，界面不会因扫描或合并而阻塞。")
	if addons == "" {
		logLine("未自动找到游戏目录，请手动选择 left4dead2\\addons。")
	} else {
		logLine("已找到游戏目录：" + addons)
	}
	logLine("JSON 状态目录：" + ui.stateDir)
	if deploymentMigrationErr != nil {
		logLine("部署记录迁移失败：" + deploymentMigrationErr.Error())
	} else if deploymentMigrated {
		logLine("旧版部署记录已升级为多目录格式。")
	}
}

func makeControl(parent uintptr, class, text string, style uint32, x, y, width, height, id int) uintptr {
	handle, _, _ := procCreateWindow.Call(0, uintptr(unsafe.Pointer(utf16(class))), uintptr(unsafe.Pointer(utf16(text))),
		uintptr(style), uintptr(x), uintptr(y), uintptr(width), uintptr(height), parent, uintptr(id), 0, 0)
	if handle != 0 && ui.font != 0 {
		procSendMessage.Call(handle, wmSetFont, ui.font, 1)
	}
	return handle
}
func makeLabel(parent uintptr, text string, x, y, width, height int) uintptr {
	return makeControl(parent, "STATIC", text, wsChild|wsVisible, x, y, width, height, 0)
}
func makeButton(parent uintptr, text string, x, y, width, height, id int) uintptr {
	return makeControl(parent, "BUTTON", text, wsChild|wsVisible|wsTabStop|bsPushButton, x, y, width, height, id)
}

func handleCommand(id int) {
	switch id {
	case idBrowseSrc:
		if path := browseFolder(ui.hwnd, "选择包含 VPK 的 MOD 目录"); path != "" {
			setText(ui.source, path)
		}
	case idBrowseOut:
		if path := browseFolder(ui.hwnd, "选择合并包输出目录"); path != "" {
			setText(ui.output, path)
		}
	case idBrowseGame:
		if path := browseFolder(ui.hwnd, "选择 left4dead2\\addons 目录"); path != "" {
			setText(ui.addons, path)
		}
	case idScan:
		startTask("scan")
	case idMerge:
		if ui.scanResult == nil {
			logLine("请先点击“智能扫描 MOD”，生成当前目录的动态分类方案。")
			return
		}
		groups, existing, err := unresolvedConflictGroups(ui.stateDir, *ui.scanResult)
		if err != nil {
			logLine("无法读取冲突策略：" + err.Error())
			return
		}
		if len(groups) == 0 {
			startTask("merge")
		} else {
			openConflictResolver(groups, existing, *ui.scanResult, ui.stateDir)
		}
	case idDeploy:
		startTask("deploy")
	case idRestore:
		startTask("restore")
	}
}

func startTask(kind string) {
	if ui.busy {
		return
	}
	ui.busy = true
	setButtons(false)
	procSendMessage.Call(ui.progress, pbmSetPos, 0, 0)
	source, output, addons := getText(ui.source), getText(ui.output), getText(ui.addons)
	scanResult := ui.scanResult
	go func() {
		defer postEvent(appEvent{Kind: "done"})
		switch kind {
		case "scan":
			if cleanPath(source) == cleanPath(output) {
				postEvent(appEvent{Kind: "error", Text: "来源目录和输出目录不能相同"})
				break
			}
			if pathWithin(output, addons) {
				postEvent(appEvent{Kind: "error", Text: "输出目录不能位于游戏 addons 内部"})
				break
			}
			postEvent(appEvent{Kind: "log", Text: "正在读取全部 VPK 目录并分析资源类型……"})
			result, err := modscan.Scan(source)
			if err != nil {
				postEvent(appEvent{Kind: "error", Text: "扫描失败：" + err.Error()})
			} else {
				if mkdirErr := os.MkdirAll(ui.stateDir, 0755); mkdirErr != nil {
					postEvent(appEvent{Kind: "error", Text: "无法创建 EXE 同级状态目录：" + mkdirErr.Error()})
					break
				}
				if reportErr := writeJSONAtomic(filepath.Join(ui.stateDir, scanReportName), result); reportErr != nil {
					postEvent(appEvent{Kind: "error", Text: "无法写入扫描报告：" + reportErr.Error()})
					break
				}
				if policyErr := writeConflictPolicy(ui.stateDir, result); policyErr != nil {
					postEvent(appEvent{Kind: "error", Text: "无法写入冲突策略：" + policyErr.Error()})
					break
				}
				if cleanupErr := removeLegacyOutputJSON(output, ui.stateDir); cleanupErr != nil {
					postEvent(appEvent{Kind: "log", Text: "旧 JSON 保留未删除：" + cleanupErr.Error()})
				}
				postEvent(appEvent{Kind: "scan", Data: result})
			}
		case "merge":
			if cleanPath(source) == cleanPath(output) {
				postEvent(appEvent{Kind: "error", Text: "来源目录和输出目录不能相同"})
				break
			}
			if pathWithin(output, addons) {
				postEvent(appEvent{Kind: "error", Text: "输出目录不能位于游戏 addons 内部"})
				break
			}
			if scanResult == nil || cleanPath(source) != cleanPath(scanResult.Directory) {
				postEvent(appEvent{Kind: "error", Text: "来源目录与扫描结果不一致，请重新扫描"})
				break
			}
			currentFingerprint, fingerprintErr := modscan.Fingerprint(source)
			if fingerprintErr != nil || currentFingerprint != scanResult.Fingerprint {
				postEvent(appEvent{Kind: "error", Text: "源 MOD 在扫描后发生变化，请重新扫描"})
				break
			}
			selections, selectionErr := loadConflictSelections(ui.stateDir, scanResult.Fingerprint)
			if selectionErr != nil {
				postEvent(appEvent{Kind: "error", Text: selectionErr.Error()})
				break
			}
			plan, planErr := scanResult.Plan(output, selections)
			if planErr != nil {
				postEvent(appEvent{Kind: "error", Text: planErr.Error()})
				break
			}
			// Any new build attempt invalidates the previous deployable state.
			_ = os.Remove(filepath.Join(ui.stateDir, buildManifestName))
			cleanup, err := prepareOverlays(&plan, scanResult)
			if cleanup != nil {
				defer cleanup()
			}
			if err == nil {
				postEvent(appEvent{Kind: "log", Text: "开始分类合并……"})
				err = vpkmerge.Run(plan, func(progress vpkmerge.Progress) {
					postEvent(appEvent{Kind: "progress", Current: int64(progress.GroupIndex), Total: int64(progress.GroupCount),
						Text: fmt.Sprintf("[%d/%d] %s · %d 文件 · %.1f MiB", progress.GroupIndex, progress.GroupCount, progress.Output, progress.FileCount, float64(progress.Bytes)/1048576)})
				})
			}
			if err != nil {
				postEvent(appEvent{Kind: "error", Text: "合并失败：" + err.Error()})
			} else {
				policyDigest, digestErr := hashFile(filepath.Join(ui.stateDir, conflictPolicyName))
				if digestErr != nil {
					postEvent(appEvent{Kind: "error", Text: "冲突策略校验失败：" + digestErr.Error()})
				} else if _, manifestErr := createBuildManifest(plan, *scanResult, policyDigest, ui.stateDir); manifestErr != nil {
					postEvent(appEvent{Kind: "error", Text: "构建清单生成失败：" + manifestErr.Error()})
				} else {
					if cleanupErr := removeLegacyOutputJSON(output, ui.stateDir); cleanupErr != nil {
						postEvent(appEvent{Kind: "log", Text: "旧 JSON 保留未删除：" + cleanupErr.Error()})
					}
					postEvent(appEvent{Kind: "log", Text: "全部分类包合并完成，并已生成校验清单。"})
				}
			}
		case "deploy":
			if gameRunning() {
				postEvent(appEvent{Kind: "error", Text: "部署已阻止：请先完全退出 Left 4 Dead 2"})
				break
			}
			if cleanPath(source) == cleanPath(output) {
				postEvent(appEvent{Kind: "error", Text: "部署已阻止：来源目录和输出目录不能相同"})
				break
			}
			if scanResult == nil || cleanPath(source) != cleanPath(scanResult.Directory) {
				postEvent(appEvent{Kind: "error", Text: "部署已阻止：来源目录与扫描结果不一致，请重新扫描"})
				break
			}
			manifest, validateErr := validateBuildProgress(output, ui.stateDir, scanResult, func(current, total int64, text string) {
				// Build validation occupies the first 20% of deployment.
				scaled := int64(0)
				if total > 0 {
					scaled = current * 20 / total
				}
				postEvent(appEvent{Kind: "progress", Current: scaled, Total: 100, Text: text})
			})
			if validateErr != nil {
				postEvent(appEvent{Kind: "error", Text: "部署已阻止：" + validateErr.Error()})
				break
			}
			backup, localDuplicates, err := deployAndDisable(manifest, output, addons, ui.stateDir, func(current, total int64, text string) {
				// Staging, duplicate detection and commit occupy the final 80%.
				scaled := int64(20)
				if total > 0 {
					scaled += current * 80 / total
				}
				postEvent(appEvent{Kind: "progress", Current: scaled, Total: 100, Text: text})
			})
			if err != nil {
				postEvent(appEvent{Kind: "error", Text: "部署失败：" + err.Error()})
			} else {
				if len(localDuplicates) > 0 {
					postEvent(appEvent{Kind: "log", Text: fmt.Sprintf(
						"已识别并禁用 %d 个 addons 根目录中的重复非订阅 MOD：%s",
						len(localDuplicates), strings.Join(localDuplicates, ", "),
					)})
				}
				postEvent(appEvent{Kind: "log", Text: "部署完成：原订阅 MOD 与重复非订阅 MOD 已设为禁用，合并包已启用。备份：" + backup})
			}
		case "restore":
			if gameRunning() {
				postEvent(appEvent{Kind: "error", Text: "还原已阻止：请先完全退出 Left 4 Dead 2"})
				break
			}
			backup, err := restoreLatest(addons, ui.stateDir, func(current, total int64, text string) {
				postEvent(appEvent{Kind: "progress", Current: current, Total: total, Text: text})
			})
			if err != nil {
				postEvent(appEvent{Kind: "error", Text: "恢复失败：" + err.Error()})
			} else {
				postEvent(appEvent{Kind: "log", Text: "已从备份恢复 addonlist：" + backup})
			}
		}
	}()
}

func prepareOverlays(plan *vpkmerge.Plan, scanResult *modscan.Result) (func(), error) {
	dir, err := os.MkdirTemp("", "l4d2modjoin-overlays-")
	if err != nil {
		return nil, err
	}
	files := map[string]string{"@gameplay": gameplayOverlay, "@sprays": spraysOverlay}
	paths := map[string]string{}
	for key, content := range files {
		path := filepath.Join(dir, strings.TrimPrefix(key, "@")+".txt")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
		paths[key] = path
	}
	for index := range plan.Groups {
		for target, source := range plan.Groups[index].Overlay {
			if path, ok := paths[source]; ok {
				plan.Groups[index].Overlay[target] = path
			}
		}
	}
	if scanResult != nil {
		handlers := map[string]func([][]byte) []byte{
			"scripts/vscripts/director_base_addon.nut": mergeScriptEntries,
			"scripts/sprays_manifest.txt":              mergeKeyValues,
		}
		for _, conflict := range scanResult.Conflicts {
			handler := handlers[conflict.Path]
			if handler == nil || conflict.Identical || !conflict.SafeMerge {
				continue
			}
			var contents [][]byte
			for _, packageName := range conflict.Packages {
				content, readErr := vpkmerge.ReadFile(filepath.Join(plan.Input, packageName), conflict.Path)
				if readErr != nil {
					os.RemoveAll(dir)
					return nil, readErr
				}
				contents = append(contents, content)
			}
			merged := handler(contents)
			path := filepath.Join(dir, strings.NewReplacer("/", "_", "\\", "_").Replace(conflict.Path))
			if writeErr := os.WriteFile(path, merged, 0644); writeErr != nil {
				os.RemoveAll(dir)
				return nil, writeErr
			}
			targetPackage := conflict.Packages[0]
			for index := range plan.Groups {
				if contains(plan.Groups[index].Packages, targetPackage) {
					if plan.Groups[index].Overlay == nil {
						plan.Groups[index].Overlay = map[string]string{}
					}
					plan.Groups[index].Overlay[conflict.Path] = path
					break
				}
			}
		}
	}
	return func() { os.RemoveAll(dir) }, nil
}

func mergeScriptEntries(contents [][]byte) []byte {
	seen := map[string]bool{}
	var lines []string
	lines = append(lines, "// Automatically merged by L4D2 Mod Join.")
	for _, content := range contents {
		for _, line := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "//") || seen[line] {
				continue
			}
			// Scan only marks this conflict safe when every non-comment line is
			// a standalone IncludeScript call.
			seen[line] = true
			lines = append(lines, line)
		}
	}
	return []byte(strings.Join(lines, "\r\n") + "\r\n")
}

func mergeKeyValues(contents [][]byte) []byte {
	pair := regexp.MustCompile(`^\s*"([^"]+)"\s+"([^"]+)"\s*$`)
	seen := map[string]bool{}
	var body []string
	for _, content := range contents {
		text := strings.ReplaceAll(string(content), "\r\n", "\n")
		start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
		if start < 0 || end <= start {
			continue
		}
		for _, line := range strings.Split(text[start+1:end], "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "//") {
				continue
			}
			match := pair.FindStringSubmatch(trimmed)
			if match == nil || seen[match[1]] {
				continue
			}
			seen[match[1]] = true
			body = append(body, fmt.Sprintf("\t%q %q", match[1], match[2]))
		}
	}
	return []byte("sprays_manifest\r\n{\r\n" + strings.Join(body, "\r\n") + "\r\n}\r\n")
}

func postEvent(event appEvent) {
	id := atomic.AddUint64(&eventID, 1)
	eventValues.Store(id, event)
	procPostMessage.Call(ui.hwnd, wmAppEvent, uintptr(id), 0)
}
func handleEvent(id uint64) {
	value, ok := eventValues.LoadAndDelete(id)
	if !ok {
		return
	}
	event := value.(appEvent)
	switch event.Kind {
	case "progress":
		position := int64(0)
		if event.Total > 0 {
			position = event.Current * 100 / event.Total
		}
		procSendMessage.Call(ui.progress, pbmSetPos, uintptr(position), 0)
		if event.Text != "" {
			logLine(event.Text)
		}
	case "error":
		logLine("错误 · " + event.Text)
	case "log":
		logLine(event.Text)
	case "scan":
		result := event.Data.(modscan.Result)
		ui.scanResult = &result
		different, identical := 0, 0
		for _, conflict := range result.Conflicts {
			if conflict.Identical {
				identical++
			} else {
				different++
				mode := "需要在策略文件中选择"
				if conflict.SafeMerge {
					mode = "可安全自动合并"
				}
				logLine("冲突 · " + conflict.Path + " · " + mode +
					" · 来源：" + strings.Join(conflict.Packages, ", "))
			}
		}
		logLine(fmt.Sprintf("智能扫描完成：%d 个 MOD，%d 个分类；%d 个同内容重复，%d 个不同内容冲突。",
			len(result.Packages), len(result.Categories), identical, different))
		if different > 0 {
			logLine("冲突策略：" + filepath.Join(ui.stateDir, conflictPolicyName) +
				"；请为非安全冲突填写 selected 后再合并。")
		}
		for _, category := range result.Categories {
			logLine(fmt.Sprintf("分类 · %s：%d 个 MOD → %s",
				category.Title, len(category.Packages), category.Output))
		}
		if len(result.UnknownPackages) > 0 {
			logLine("未明确识别的 MOD 已放入 Misc：" + strings.Join(result.UnknownPackages, ", "))
		}
	case "done":
		ui.busy = false
		setButtons(true)
	case "merge-ready":
		startTask("merge")
	}
}

func setButtons(enabled bool) {
	value := uintptr(0)
	if enabled {
		value = 1
	}
	for _, handle := range []uintptr{ui.scan, ui.merge, ui.deploy, ui.restore} {
		procEnableWindow.Call(handle, value)
	}
}
func logLine(text string) {
	// Newest messages live at index 0. Preserve the user's reading position
	// when they have scrolled down into older messages.
	top, _, _ := procSendMessage.Call(ui.log, lbGetTopIndex, 0, 0)
	procSendMessage.Call(ui.log, wmSetRedraw, 0, 0)
	procSendMessage.Call(ui.log, lbInsertString, 0, uintptr(unsafe.Pointer(utf16(text))))
	if top == 0 || top == ^uintptr(0) {
		procSendMessage.Call(ui.log, lbSetTopIndex, 0, 0)
	} else {
		procSendMessage.Call(ui.log, lbSetTopIndex, top+1, 0)
	}
	procSendMessage.Call(ui.log, wmSetRedraw, 1, 0)
	// The listbox repaints its own complete client area. Avoid requesting a
	// separate background erase, which causes a visible flash on rapid updates.
	procInvalidateRect.Call(ui.log, 0, 0)
}
func setText(handle uintptr, text string) {
	procSetWindowText.Call(handle, uintptr(unsafe.Pointer(utf16(text))))
}
func getText(handle uintptr) string {
	length, _, _ := procGetWindowLen.Call(handle)
	buffer := make([]uint16, length+1)
	procGetWindowText.Call(handle, uintptr(unsafe.Pointer(&buffer[0])), length+1)
	return syscall.UTF16ToString(buffer)
}

func browseFolder(owner uintptr, title string) string {
	buffer := make([]uint16, 260)
	info := browseInfo{Owner: owner, DisplayName: &buffer[0], Title: utf16(title), Flags: 0x0001 | 0x0040}
	pidl, _, _ := procBrowseFolder.Call(uintptr(unsafe.Pointer(&info)))
	if pidl == 0 {
		return ""
	}
	defer procCoTaskMemFree.Call(pidl)
	path := make([]uint16, 32768)
	ok, _, _ := procGetPathPIDL.Call(pidl, uintptr(unsafe.Pointer(&path[0])))
	if ok == 0 {
		return ""
	}
	return syscall.UTF16ToString(path)
}

func paintWindow(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	var client rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	width := client.Right
	height := client.Bottom
	if width <= 0 || height <= 0 {
		procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return
	}
	memoryDC, _, _ := procCreateCompatDC.Call(hdc)
	bitmap, _, _ := procCreateCompatBM.Call(hdc, uintptr(width), uintptr(height))
	oldBitmap, _, _ := procSelectObject.Call(memoryDC, bitmap)
	bg, _, _ := procCreateBrush.Call(0x00241E1B)
	header, _, _ := procCreateBrush.Call(0x00352B25)
	procFillRect.Call(memoryDC, uintptr(unsafe.Pointer(&client)), bg)
	headerRect := rect{0, 0, width, 94}
	procFillRect.Call(memoryDC, uintptr(unsafe.Pointer(&headerRect)), header)
	procSetBkMode.Call(memoryDC, 1)
	procSetTextColor.Call(memoryDC, 0x00F3F0EC)
	oldFont, _, _ := procSelectObject.Call(memoryDC, ui.titleFont)
	title := "L4D2 MOD JOIN V2"
	procTextOut.Call(memoryDC, 42, 24, uintptr(unsafe.Pointer(utf16(title))), uintptr(len([]rune(title))))
	procSelectObject.Call(memoryDC, ui.font)
	procSetTextColor.Call(memoryDC, 0x00AFA8A2)
	subtitle := "分类合并 · 冲突控制 · 一键部署"
	procTextOut.Call(memoryDC, 43, 63, uintptr(unsafe.Pointer(utf16(subtitle))), uintptr(len([]rune(subtitle))))
	procSelectObject.Call(memoryDC, oldFont)
	procBitBlt.Call(hdc, 0, 0, uintptr(width), uintptr(height), memoryDC, 0, 0, 0x00CC0020)
	procSelectObject.Call(memoryDC, oldBitmap)
	procDeleteObject.Call(bitmap)
	procDeleteDC.Call(memoryDC)
	procDeleteObject.Call(bg)
	procDeleteObject.Call(header)
	procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
}
