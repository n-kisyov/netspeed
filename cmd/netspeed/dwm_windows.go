//go:build windows

package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
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
	procOpenClipboard       = user32.NewProc("OpenClipboard")
	procCloseClipboard      = user32.NewProc("CloseClipboard")
	procEmptyClipboard      = user32.NewProc("EmptyClipboard")
	procSetClipboardData    = user32.NewProc("SetClipboardData")
	cbKernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalAlloc         = cbKernel32.NewProc("GlobalAlloc")
	procGlobalFree          = cbKernel32.NewProc("GlobalFree")
	procGlobalLock          = cbKernel32.NewProc("GlobalLock")
	procGlobalUnlock        = cbKernel32.NewProc("GlobalUnlock")
)

const (
	dwmwaUseImmersiveDarkMode = 20
	dwmwaWindowCornerPref     = 33
	dwmwaCaptionColor         = 35
	dwmWcpRound               = 2
	swHide                    = 0
	swShow                    = 5
	swRestore                 = 9
	gmemMoveable              = 0x0002
	cfUnicodeText             = 13
	startupRunKeyPath         = `Software\Microsoft\Windows\CurrentVersion\Run`
	startupValueName          = "NetSpeed"
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

// SetClipboardText reliably writes UTF-16 text with Windows CRLF line endings to the clipboard.
func SetClipboardText(text string) error {
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	u16, err := windows.UTF16FromString(normalized)
	if err != nil {
		return err
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var opened bool
	var openErr error
	for attempt := 0; attempt < 5; attempt++ {
		r, _, e := procOpenClipboard.Call(0)
		if r != 0 {
			opened = true
			break
		}
		openErr = e
		time.Sleep(15 * time.Millisecond)
	}
	if !opened {
		return fmt.Errorf("OpenClipboard failed: %v", openErr)
	}
	defer procCloseClipboard.Call()

	if r, _, e := procEmptyClipboard.Call(); r == 0 {
		return fmt.Errorf("EmptyClipboard failed: %v", e)
	}

	byteLen := uintptr(len(u16) * 2)
	hMem, _, allocErr := procGlobalAlloc.Call(gmemMoveable, byteLen)
	if hMem == 0 {
		return fmt.Errorf("GlobalAlloc failed: %v", allocErr)
	}

	ptr, _, lockErr := procGlobalLock.Call(hMem)
	if ptr == 0 {
		_, _, _ = procGlobalFree.Call(hMem)
		return fmt.Errorf("GlobalLock failed: %v", lockErr)
	}
	dst := unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(u16))
	copy(dst, u16)
	_, _, _ = procGlobalUnlock.Call(hMem)

	r, _, setErr := procSetClipboardData.Call(cfUnicodeText, hMem)
	if r == 0 {
		_, _, _ = procGlobalFree.Call(hMem)
		return fmt.Errorf("SetClipboardData failed: %v", setErr)
	}
	return nil
}

// IsStartupEnabled reports whether NetSpeed is registered to launch on Windows login.
func IsStartupEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, startupRunKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	val, _, err := k.GetStringValue(startupValueName)
	return err == nil && strings.TrimSpace(val) != ""
}

// SetStartupEnabled registers or unregisters NetSpeed (--minimized) in the HKCU Run key.
func SetStartupEnabled(enable bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, startupRunKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if !enable {
		err := k.DeleteValue(startupValueName)
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}

	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf("%q --minimized", exePath)
	return k.SetStringValue(startupValueName, cmd)
}
