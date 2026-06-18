//go:build windows

package main

import (
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32Process     = syscall.NewLazyDLL("kernel32.dll")
	procSnapshot        = kernel32Process.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW = kernel32Process.NewProc("Process32FirstW")
	procProcess32NextW  = kernel32Process.NewProc("Process32NextW")
	procCloseHandle     = kernel32Process.NewProc("CloseHandle")
)

type processEntry32 struct {
	Size            uint32
	Usage           uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}

func gameRunning() bool {
	snapshot, _, _ := procSnapshot.Call(0x00000002, 0)
	if snapshot == ^uintptr(0) || snapshot == 0 {
		return false
	}
	defer procCloseHandle.Call(snapshot)
	var entry processEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	ok, _, _ := procProcess32FirstW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	for ok != 0 {
		name := strings.ToLower(syscall.UTF16ToString(entry.ExeFile[:]))
		if name == "left4dead2.exe" {
			return true
		}
		ok, _, _ = procProcess32NextW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	}
	return false
}
