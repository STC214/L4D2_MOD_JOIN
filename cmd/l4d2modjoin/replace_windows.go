//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	kernel32StateMove = syscall.NewLazyDLL("kernel32.dll")
	procStateMoveFile = kernel32StateMove.NewProc("MoveFileExW")
)

func replaceStateFile(source, destination string) error {
	src, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	dst, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	result, _, callErr := procStateMoveFile.Call(
		uintptr(unsafe.Pointer(src)),
		uintptr(unsafe.Pointer(dst)),
		0x1|0x8,
	)
	if result == 0 {
		return callErr
	}
	return nil
}
