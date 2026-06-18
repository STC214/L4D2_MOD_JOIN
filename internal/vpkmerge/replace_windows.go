//go:build windows

package vpkmerge

import (
	"syscall"
	"unsafe"
)

var (
	kernel32MoveFile = syscall.NewLazyDLL("kernel32.dll")
	procMoveFileExW  = kernel32MoveFile.NewProc("MoveFileExW")
)

func replaceFileAtomic(source, destination string) error {
	src, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	dst, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	result, _, callErr := procMoveFileExW.Call(
		uintptr(unsafe.Pointer(src)),
		uintptr(unsafe.Pointer(dst)),
		0x1|0x8, // MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH
	)
	if result == 0 {
		return callErr
	}
	return nil
}
