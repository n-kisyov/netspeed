//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	dwmapi                  = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttr    = dwmapi.NewProc("DwmSetWindowAttribute")
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procShowWindowAsync     = user32.NewProc("ShowWindowAsync")
	procIsIconic            = user32.NewProc("IsIconic")
	procIsWindowVisible     = user32.NewProc("IsWindowVisible")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procEnumWindows         = user32.NewProc("EnumWindows")
	procGetWindowThreadPID  = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowTextLength = user32.NewProc("GetWindowTextLengthW")
)

const (
	dwmwaUseImmersiveDarkMode  = 20
	dwmwaWindowCornerPref      = 33
	dwmwaCaptionColor          = 35
	dwmWcpRound                = 2
	swHide                     = 0
	swShow                     = 5
	swRestore                  = 9
)

// ApplyWindowTheme configures Windows 11 DWM dark/light titlebar and rounded corners.
func ApplyWindowTheme(hwnd uintptr, dark bool) {
	if hwnd == 0 {
		return
	}
	var darkVal uint32
	var captionColor uint32
	if dark {
		darkVal = 1
		captionColor = 0x00170F0B // COLORREF BGR for #0B0F17
	} else {
		darkVal = 0
		captionColor = 0x00FCFAF8 // COLORREF BGR for #F8FAFC
	}

	_, _, _ = procDwmSetWindowAttr.Call(
		hwnd,
		dwmwaUseImmersiveDarkMode,
		uintptr(unsafe.Pointer(&darkVal)),
		unsafe.Sizeof(darkVal),
	)
	corner := uint32(dwmWcpRound)
	_, _, _ = procDwmSetWindowAttr.Call(
		hwnd,
		dwmwaWindowCornerPref,
		uintptr(unsafe.Pointer(&corner)),
		unsafe.Sizeof(corner),
	)
	_, _, _ = procDwmSetWindowAttr.Call(
		hwnd,
		dwmwaCaptionColor,
		uintptr(unsafe.Pointer(&captionColor)),
		unsafe.Sizeof(captionColor),
	)
}

// ShowWindowFromTray restores and focuses the main Gio window asynchronously.
func ShowWindowFromTray(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	_, _, _ = procShowWindowAsync.Call(hwnd, swShow)
	iconic, _, _ := procIsIconic.Call(hwnd)
	if iconic != 0 {
		_, _, _ = procShowWindowAsync.Call(hwnd, swRestore)
	}
	_, _, _ = procSetForegroundWindow.Call(hwnd)
}

// HideWindowToTray hides the main window asynchronously without blocking the Gio window thread.
func HideWindowToTray(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	_, _, _ = procShowWindowAsync.Call(hwnd, swHide)
}

// IsWindowVisible reports whether the given HWND is currently visible.
func IsWindowVisible(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	r, _, _ := procIsWindowVisible.Call(hwnd)
	return r != 0
}

// FindProcessMainWindow locates a visible or titled top-level window belonging to this process.
func FindProcessMainWindow() uintptr {
	pid := windows.GetCurrentProcessId()
	var found uintptr
	cb := syscall.NewCallback(func(hwnd, lparam uintptr) uintptr {
		var winPID uint32
		_, _, _ = procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&winPID)))
		if winPID == pid {
			titleLen, _, _ := procGetWindowTextLength.Call(hwnd)
			if titleLen > 0 {
				found = hwnd
				return 0 // stop enumeration
			}
		}
		return 1
	})
	_, _, _ = procEnumWindows.Call(cb, 0)
	return found
}
