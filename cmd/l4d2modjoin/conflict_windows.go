//go:build windows

package main

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"l4d2-mod-join/internal/modscan"
)

const (
	idConflictList      = 2101
	idConflictCombo     = 2102
	idConflictRecommend = 2103
	idConflictConfirm   = 2104
	idConflictCancel    = 2105
	lbnSelChange        = 1
	cbnSelChange        = 1
	cbAddString         = 0x0143
	cbGetCurSel         = 0x0147
	cbResetContent      = 0x014B
	cbSetCurSel         = 0x014E
	wsVScroll           = 0x00200000
	cbsDropDownList     = 0x0003
	ssLeft              = 0x00000000
)

type conflictResolver struct {
	hwnd, list, combo, detail, status uintptr
	groups                            []modscan.ConflictGroup
	selections                        map[string]string
	result                            modscan.Result
	output                            string
	current                           int
}

var resolver *conflictResolver

func registerConflictClass(instance, iconLarge, iconSmall uintptr) {
	className := utf16("L4D2ModJoinConflictWindow")
	wc := wndClassEx{
		Size: uint32(unsafe.Sizeof(wndClassEx{})), WndProc: syscall.NewCallback(conflictWindowProc),
		Instance: instance, Icon: iconLarge, IconSm: iconSmall,
		Background: colorWindow + 1, ClassName: className,
	}
	procRegisterClass.Call(uintptr(unsafe.Pointer(&wc)))
}

func openConflictResolver(groups []modscan.ConflictGroup, existing map[string]string, result modscan.Result, output string) {
	if len(groups) == 0 || resolver != nil {
		return
	}
	selections := map[string]string{}
	for _, group := range groups {
		selected := ""
		for _, path := range group.Paths {
			if contains(group.Packages, existing[path]) {
				selected = existing[path]
				break
			}
		}
		if selected == "" {
			selected = group.Recommended
		}
		for _, path := range group.Paths {
			selections[path] = selected
		}
	}
	resolver = &conflictResolver{
		groups: groups, selections: selections, result: result, output: output, current: 0,
	}
	procEnableWindow.Call(ui.hwnd, 0)
	hwnd, _, _ := procCreateWindow.Call(
		0,
		uintptr(unsafe.Pointer(utf16("L4D2ModJoinConflictWindow"))),
		uintptr(unsafe.Pointer(utf16("批量处理 MOD 冲突"))),
		wsOverlapped|wsClipChildren,
		260, 150, 860, 610,
		ui.hwnd, 0, 0, 0,
	)
	if hwnd == 0 {
		resolver = nil
		procEnableWindow.Call(ui.hwnd, 1)
		logLine("无法创建冲突处理窗口。")
	} else {
		enableDarkTitleBar(hwnd)
		procShowWindow.Call(hwnd, swShow)
		procUpdateWindow.Call(hwnd)
	}
}

func conflictWindowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmCreate:
		resolver.hwnd = hwnd
		createConflictUI(hwnd)
		return 0
	case wmCommand:
		id := int(wParam & 0xffff)
		code := int((wParam >> 16) & 0xffff)
		switch {
		case id == idConflictList && code == lbnSelChange:
			index, _, _ := procSendMessage.Call(resolver.list, 0x0188, 0, 0)
			if int(index) >= 0 && int(index) < len(resolver.groups) {
				resolver.current = int(index)
				refreshConflictDetails()
			}
		case id == idConflictCombo && code == cbnSelChange:
			updateCurrentSelection()
		case id == idConflictRecommend:
			applyAllRecommendations()
		case id == idConflictConfirm:
			confirmConflictSelections()
		case id == idConflictCancel:
			closeConflictResolver(false)
		}
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
		paintConflictWindow(hwnd)
		return 0
	case wmEraseBkgnd:
		return 1
	case wmClose:
		closeConflictResolver(false)
		return 0
	case wmDestroy:
		return 0
	}
	result, _, _ := procDefWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
	return result
}

func createConflictUI(hwnd uintptr) {
	makeLabel(hwnd, "以下冲突已按竞争 MOD 聚合。每组选择一次，会应用到该组全部冲突文件。", 30, 96, 790, 26)
	makeLabel(hwnd, "左侧选择冲突组，右侧选择要保留的 MOD。程序已预选推荐项，可一次确认。", 30, 124, 790, 24)
	resolver.list = makeControl(hwnd, "LISTBOX", "",
		wsChild|wsVisible|wsBorder|wsVScroll|lbsNoIntegral, 30, 158, 330, 320, idConflictList)
	resolver.detail = makeControl(hwnd, "STATIC", "",
		wsChild|wsVisible|ssLeft, 390, 162, 420, 160, 0)
	makeLabel(hwnd, "保留此 MOD 的冲突版本：", 390, 338, 300, 24)
	resolver.combo = makeControl(hwnd, "COMBOBOX", "",
		wsChild|wsVisible|wsBorder|wsTabStop|cbsDropDownList, 390, 366, 420, 200, idConflictCombo)
	resolver.status = makeControl(hwnd, "STATIC", "", wsChild|wsVisible, 390, 420, 420, 58, 0)
	makeButton(hwnd, "全部采用推荐", 30, 505, 170, 42, idConflictRecommend)
	makeButton(hwnd, "取消", 590, 505, 100, 42, idConflictCancel)
	makeButton(hwnd, "确认并开始合并", 704, 505, 140, 42, idConflictConfirm)

	for index, group := range resolver.groups {
		label := fmt.Sprintf("%02d  %d 个冲突文件 · %d 个 MOD", index+1, len(group.Paths), len(group.Packages))
		procSendMessage.Call(resolver.list, lbAddString, 0, uintptr(unsafe.Pointer(utf16(label))))
	}
	procSendMessage.Call(resolver.list, lbSetCurSel, 0, 0)
	refreshConflictDetails()
}

func paintConflictWindow(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	var client rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	width, height := client.Right, client.Bottom
	if width <= 0 || height <= 0 {
		procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return
	}
	memoryDC, _, _ := procCreateCompatDC.Call(hdc)
	bitmap, _, _ := procCreateCompatBM.Call(hdc, uintptr(width), uintptr(height))
	oldBitmap, _, _ := procSelectObject.Call(memoryDC, bitmap)
	procFillRect.Call(memoryDC, uintptr(unsafe.Pointer(&client)), ui.bgBrush)
	header, _, _ := procCreateBrush.Call(0x00352B25)
	headerRect := rect{0, 0, width, 76}
	procFillRect.Call(memoryDC, uintptr(unsafe.Pointer(&headerRect)), header)
	procSetBkMode.Call(memoryDC, 1)
	procSetTextColor.Call(memoryDC, 0x00F3F0EC)
	oldFont, _, _ := procSelectObject.Call(memoryDC, ui.titleFont)
	title := "批量处理 MOD 冲突"
	procTextOut.Call(memoryDC, 30, 18, uintptr(unsafe.Pointer(utf16(title))), uintptr(len([]rune(title))))
	procSelectObject.Call(memoryDC, ui.font)
	procSetTextColor.Call(memoryDC, 0x00AFA8A2)
	subtitle := "聚合决策 · 推荐提示 · 一次确认"
	procTextOut.Call(memoryDC, 31, 52, uintptr(unsafe.Pointer(utf16(subtitle))), uintptr(len([]rune(subtitle))))
	procSelectObject.Call(memoryDC, oldFont)
	procBitBlt.Call(hdc, 0, 0, uintptr(width), uintptr(height), memoryDC, 0, 0, 0x00CC0020)
	procSelectObject.Call(memoryDC, oldBitmap)
	procDeleteObject.Call(bitmap)
	procDeleteDC.Call(memoryDC)
	procDeleteObject.Call(header)
	procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
}

func refreshConflictDetails() {
	if resolver == nil || resolver.current < 0 || resolver.current >= len(resolver.groups) {
		return
	}
	group := resolver.groups[resolver.current]
	sample := group.Paths
	if len(sample) > 5 {
		sample = sample[:5]
	}
	detail := fmt.Sprintf(
		"冲突组 %d / %d\r\n\r\n参与 MOD：\r\n%s\r\n\r\n冲突文件：%d 个\r\n%s",
		resolver.current+1, len(resolver.groups),
		strings.Join(group.Packages, "\r\n"),
		len(group.Paths), strings.Join(sample, "\r\n"),
	)
	if len(group.Paths) > len(sample) {
		detail += fmt.Sprintf("\r\n……另有 %d 个", len(group.Paths)-len(sample))
	}
	setText(resolver.detail, detail)

	procSendMessage.Call(resolver.combo, cbResetContent, 0, 0)
	selected := resolver.selections[group.Paths[0]]
	selectedIndex := 0
	for index, name := range group.Packages {
		label := name
		if name == group.Recommended {
			label += "  （推荐）"
		}
		procSendMessage.Call(resolver.combo, cbAddString, 0, uintptr(unsafe.Pointer(utf16(label))))
		if name == selected {
			selectedIndex = index
		}
	}
	procSendMessage.Call(resolver.combo, cbSetCurSel, uintptr(selectedIndex), 0)
	setText(resolver.status, "推荐提示：\r\n"+group.Reason)
}

func updateCurrentSelection() {
	if resolver == nil || resolver.current >= len(resolver.groups) {
		return
	}
	index, _, _ := procSendMessage.Call(resolver.combo, cbGetCurSel, 0, 0)
	group := resolver.groups[resolver.current]
	if int(index) < 0 || int(index) >= len(group.Packages) {
		return
	}
	selected := group.Packages[int(index)]
	for _, path := range group.Paths {
		resolver.selections[path] = selected
	}
}

func applyAllRecommendations() {
	for _, group := range resolver.groups {
		for _, path := range group.Paths {
			resolver.selections[path] = group.Recommended
		}
	}
	refreshConflictDetails()
	setText(resolver.status, "已为全部冲突组选择推荐方案。\r\n你仍可逐组调整，然后一次确认。")
}

func confirmConflictSelections() {
	updateCurrentSelection()
	if err := saveConflictGroupSelections(resolver.output, resolver.result, resolver.selections); err != nil {
		setText(resolver.status, "保存失败："+err.Error())
		return
	}
	closeConflictResolver(true)
}

func closeConflictResolver(startMerge bool) {
	if resolver == nil {
		return
	}
	hwnd := resolver.hwnd
	resolver = nil
	procDestroyWindow := user32.NewProc("DestroyWindow")
	procDestroyWindow.Call(hwnd)
	procEnableWindow.Call(ui.hwnd, 1)
	user32.NewProc("SetForegroundWindow").Call(ui.hwnd)
	if startMerge {
		postEvent(appEvent{Kind: "merge-ready"})
	} else {
		logLine("已取消冲突选择，未开始合并。")
	}
}
