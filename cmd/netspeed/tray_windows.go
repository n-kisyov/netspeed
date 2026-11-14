//go:build windows

package main

import (
	"math"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32               = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIconW  = shell32.NewProc("Shell_NotifyIconW")
	gdi32                 = windows.NewLazySystemDLL("gdi32.dll")
	procCreateDIBSection  = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap      = gdi32.NewProc("CreateBitmap")
	procDeleteObject      = gdi32.NewProc("DeleteObject")
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGetModuleHandleW  = kernel32.NewProc("GetModuleHandleW")
	procRegisterClassExW  = user32.NewProc("RegisterClassExW")
	procCreateWindowExW   = user32.NewProc("CreateWindowExW")
	procDestroyWindow     = user32.NewProc("DestroyWindow")
	procDefWindowProcW    = user32.NewProc("DefWindowProcW")
	procGetMessageW       = user32.NewProc("GetMessageW")
	procTranslateMessage  = user32.NewProc("TranslateMessage")
	procDispatchMessageW  = user32.NewProc("DispatchMessageW")
	procPostMessageW      = user32.NewProc("PostMessageW")
	procPostQuitMessage   = user32.NewProc("PostQuitMessage")
	procCreatePopupMenu   = user32.NewProc("CreatePopupMenu")
	procDestroyMenu       = user32.NewProc("DestroyMenu")
	procAppendMenuW       = user32.NewProc("AppendMenuW")
	procTrackPopupMenu    = user32.NewProc("TrackPopupMenu")
	procGetCursorPos      = user32.NewProc("GetCursorPos")
	procCreateIconIndirect = user32.NewProc("CreateIconIndirect")
	procDestroyIcon       = user32.NewProc("DestroyIcon")
	procLoadIconW         = user32.NewProc("LoadIconW")
	procSendMessageW      = user32.NewProc("SendMessageW")
)

const (
	wmNull           = 0x0000
	wmDestroy        = 0x0002
	wmSetIcon        = 0x0080
	wmUser           = 0x0400
	wmTrayCallback   = wmUser + 1
	wmUpdateTip      = wmUser + 2
	wmQuitTray       = wmUser + 3
	wmLButtonUp      = 0x0202
	wmLButtonDblClk  = 0x0203
	wmRButtonUp      = 0x0205
	wmContextMenu    = 0x007B

	nimAdd           = 0x00000000
	nimModify        = 0x00000001
	nimDelete        = 0x00000002
	nifMessage       = 0x00000001
	nifIcon          = 0x00000002
	nifTip           = 0x00000004

	mfString         = 0x0000
	mfChecked        = 0x0008
	mfSeparator      = 0x0800
	tpmReturnCmd     = 0x0100
	tpmRightButton   = 0x0002
	tpmBottomAlign   = 0x0020

	cmdOpenWindow    = 1001
	cmdRunSpeedTest  = 1002
	cmdToggleSched   = 1003
	cmdToggleTheme   = 1004
	cmdExitApp       = 1005
)

type winPoint struct {
	X int32
	Y int32
}

type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      winPoint
}

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type notifyIconDataW struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uTimeoutVersion  uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     uintptr
}

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  uintptr
	hbmColor uintptr
}

type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

// TrayStateSnapshot provides dynamic labels for the tray right-click menu.
type TrayStateSnapshot struct {
	ScheduleEnabled bool
	DarkTheme       bool
	RunningTest     bool
}

// TrayHooks connects tray actions back to the application controller.
type TrayHooks struct {
	GetState       func() TrayStateSnapshot
	OnOpen         func()
	OnRunTest      func()
	OnToggleSched  func()
	OnToggleTheme  func()
	OnExit         func()
}

// TrayController manages the pure-Go Win32 system tray icon and context menu.
type TrayController struct {
	hooks   TrayHooks
	mu      sync.Mutex
	hwnd    uintptr
	hIcon   uintptr
	tipText string
	readyCh chan struct{}
	doneCh  chan struct{}
}

func NewTrayController(hooks TrayHooks) *TrayController {
	tc := &TrayController{
		hooks:   hooks,
		tipText: "NetSpeed — Ready",
		readyCh: make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	go tc.runMessageLoop()
	<-tc.readyCh
	return tc
}

func (t *TrayController) IconHandle() uintptr {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.hIcon
}

func (t *TrayController) UpdateTooltip(tip string) {
	t.mu.Lock()
	t.tipText = tip
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		_, _, _ = procPostMessageW.Call(hwnd, wmUpdateTip, 0, 0)
	}
}

func (t *TrayController) Close() {
	t.mu.Lock()
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		_, _, _ = procPostMessageW.Call(hwnd, wmQuitTray, 0, 0)
		<-t.doneCh
	}
}

// ApplyAppIcon sets the programmatic speedometer HICON on the Gio main window.
func ApplyAppIcon(hwnd uintptr, hIcon uintptr) {
	if hwnd == 0 || hIcon == 0 {
		return
	}
	_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, 0, hIcon) // ICON_SMALL
	_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, 1, hIcon) // ICON_BIG
}

func (t *TrayController) runMessageLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(t.doneCh)

	hInst, _, _ := procGetModuleHandleW.Call(0)
	className, _ := windows.UTF16PtrFromString("NetSpeedTrayWindowClass")
	winTitle, _ := windows.UTF16PtrFromString("NetSpeedTrayHidden")

	hIcon := createSpeedometerIcon()
	if hIcon == 0 {
		hIcon, _, _ = procLoadIconW.Call(0, 32512) // IDI_APPLICATION
	}

	wndProc := syscall.NewCallback(t.wndProc)
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   wndProc,
		hInstance:     hInst,
		lpszClassName: className,
	}
	_, _, _ = procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	// Create a normal hidden top-level window (not HWND_MESSAGE) so tray TrackPopupMenu works reliably.
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(winTitle)),
		0,
		0, 0, 0, 0,
		0, 0, hInst, 0,
	)

	t.mu.Lock()
	t.hwnd = hwnd
	t.hIcon = hIcon
	tip := t.tipText
	t.mu.Unlock()

	if hwnd != 0 {
		t.notifyIcon(nimAdd, hwnd, hIcon, tip)
	}
	close(t.readyCh)

	if hwnd == 0 {
		return
	}

	var msg winMsg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	t.notifyIcon(nimDelete, hwnd, 0, "")
	if hIcon != 0 {
		_, _, _ = procDestroyIcon.Call(hIcon)
	}
}

func (t *TrayController) notifyIcon(action uint32, hwnd, hIcon uintptr, tip string) {
	var nid notifyIconDataW
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = hwnd
	nid.uID = 1
	nid.uCallbackMessage = wmTrayCallback

	switch action {
	case nimAdd:
		nid.uFlags = nifMessage | nifIcon | nifTip
		nid.hIcon = hIcon
		copyUTF16Tip(&nid.szTip, tip)
	case nimModify:
		nid.uFlags = nifTip
		copyUTF16Tip(&nid.szTip, tip)
	case nimDelete:
		nid.uFlags = 0
	}
	_, _, _ = procShellNotifyIconW.Call(uintptr(action), uintptr(unsafe.Pointer(&nid)))
}

func copyUTF16Tip(dst *[128]uint16, s string) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return
	}
	n := len(u)
	if n > len(dst) {
		n = len(dst)
	}
	copy(dst[:], u[:n])
	dst[len(dst)-1] = 0
}

func (t *TrayController) wndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmTrayCallback:
		event := uint32(lParam & 0xFFFF)
		switch event {
		case wmLButtonUp, wmLButtonDblClk:
			if t.hooks.OnOpen != nil {
				go t.hooks.OnOpen()
			}
		case wmRButtonUp, wmContextMenu:
			t.showContextMenu(hwnd)
		}
		return 0
	case wmUpdateTip:
		t.mu.Lock()
		tip := t.tipText
		t.mu.Unlock()
		t.notifyIcon(nimModify, hwnd, 0, tip)
		return 0
	case wmQuitTray:
		_, _, _ = procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return r
}

func (t *TrayController) showContextMenu(hwnd uintptr) {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	var st TrayStateSnapshot
	if t.hooks.GetState != nil {
		st = t.hooks.GetState()
	}

	appendItem := func(flags uint32, id uintptr, label string) {
		if label == "" {
			_, _, _ = procAppendMenuW.Call(hMenu, uintptr(flags), id, 0)
			return
		}
		ptr, _ := windows.UTF16PtrFromString(label)
		_, _, _ = procAppendMenuW.Call(hMenu, uintptr(flags), id, uintptr(unsafe.Pointer(ptr)))
	}

	appendItem(mfString, cmdOpenWindow, "Open NetSpeed")
	if st.RunningTest {
		appendItem(mfString, cmdRunSpeedTest, "Speed Test Running...")
	} else {
		appendItem(mfString, cmdRunSpeedTest, "Run Speed Test Now")
	}

	schedFlags := uint32(mfString)
	schedLabel := "Scheduled Tests: Off"
	if st.ScheduleEnabled {
		schedFlags |= mfChecked
		schedLabel = "Scheduled Tests: On"
	}
	appendItem(schedFlags, cmdToggleSched, schedLabel)

	themeLabel := "Switch to Light Theme"
	if !st.DarkTheme {
		themeLabel = "Switch to Dark Theme"
	}
	appendItem(mfString, cmdToggleTheme, themeLabel)
	appendItem(mfSeparator, 0, "")
	appendItem(mfString, cmdExitApp, "Exit NetSpeed")

	var pt winPoint
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	_, _, _ = procSetForegroundWindow.Call(hwnd)

	cmd, _, _ := procTrackPopupMenu.Call(
		hMenu,
		tpmReturnCmd|tpmRightButton|tpmBottomAlign,
		uintptr(pt.X),
		uintptr(pt.Y),
		0,
		hwnd,
		0,
	)
	_, _, _ = procPostMessageW.Call(hwnd, wmNull, 0, 0)

	switch cmd {
	case cmdOpenWindow:
		if t.hooks.OnOpen != nil {
			go t.hooks.OnOpen()
		}
	case cmdRunSpeedTest:
		if t.hooks.OnRunTest != nil && !st.RunningTest {
			go t.hooks.OnRunTest()
		}
	case cmdToggleSched:
		if t.hooks.OnToggleSched != nil {
			go t.hooks.OnToggleSched()
		}
	case cmdToggleTheme:
		if t.hooks.OnToggleTheme != nil {
			go t.hooks.OnToggleTheme()
		}
	case cmdExitApp:
		if t.hooks.OnExit != nil {
			go t.hooks.OnExit()
		}
	}
}

// createSpeedometerIcon generates a crisp 32x32 anti-aliased ARGB speedometer gauge HICON in pure Go.
func createSpeedometerIcon() uintptr {
	const size = 32
	bi := bitmapInfoHeader{
		biSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:       size,
		biHeight:      -size, // top-down DIB
		biPlanes:      1,
		biBitCount:    32,
		biCompression: 0, // BI_RGB
	}

	var bitsPtr unsafe.Pointer
	hColor, _, _ := procCreateDIBSection.Call(
		0,
		uintptr(unsafe.Pointer(&bi)),
		0, // DIB_RGB_COLORS
		uintptr(unsafe.Pointer(&bitsPtr)),
		0, 0,
	)
	if hColor == 0 || bitsPtr == nil {
		return 0
	}
	defer procDeleteObject.Call(hColor)

	pixels := unsafe.Slice((*uint32)(bitsPtr), size*size)
	cx, cy := 15.5, 16.5

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x) - cx
			dy := float64(y) - cy
			r := math.Hypot(dx, dy)
			ang := math.Atan2(dy, dx) * 180.0 / math.Pi // -180..180, 0=right, 90=down

			// Dark rounded circular badge background
			if r <= 14.5 {
				alpha := clampByte((14.5 - r) * 255.0)
				// Base slate #131C2E -> BGRA
				pixels[y*size+x] = bgra(0x13, 0x1C, 0x2E, alpha)
			}

			// Outer 240-deg cyan gauge arc: angles from 150 deg (bottom-left) clockwise through -90 (top) to 30 deg (bottom-right)
			// In screen coords (y down): bottom-left=150, left=180/-180, top=-90, right=0, bottom-right=30
			inArc := ang >= 145 || ang <= 35
			if inArc && r >= 9.0 && r <= 12.2 {
				distEdge := math.Min(r-9.0, 12.2-r)
				alpha := clampByte(distEdge * 255.0)
				if alpha > 20 {
					// Cyan #38BDF8 -> Violet #A78BFA gradient from left to right
					t := (float64(x) - 4.0) / 24.0
					if t < 0 {
						t = 0
					}
					if t > 1 {
						t = 1
					}
					cr := uint8(float64(0x38)*(1-t) + float64(0xA7)*t)
					cg := uint8(float64(0xBD)*(1-t) + float64(0x8B)*t)
					cb := uint8(float64(0xF8)*(1-t) + float64(0xFA)*t)
					pixels[y*size+x] = bgra(cr, cg, cb, alpha)
				}
			}

			// Crisp white needle pointing up-right (-40 deg)
			needleAngle := -40.0 * math.Pi / 180.0
			nx, ny := math.Cos(needleAngle), math.Sin(needleAngle)
			proj := dx*nx + dy*ny
			perp := math.Abs(dx*(-ny) + dy*nx)
			if proj >= -1.0 && proj <= 9.5 && perp <= 1.25 {
				pixels[y*size+x] = bgra(0xF8, 0xFA, 0xFC, 255)
			}
			// Center pivot dot
			if r <= 2.3 {
				pixels[y*size+x] = bgra(0x38, 0xBD, 0xF8, 255)
			}
		}
	}

	hMask, _, _ := procCreateBitmap.Call(size, size, 1, 1, 0)
	if hMask == 0 {
		return 0
	}
	defer procDeleteObject.Call(hMask)

	ii := iconInfo{
		fIcon:    1,
		hbmMask:  hMask,
		hbmColor: hColor,
	}
	hIcon, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	return hIcon
}

func clampByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v)
}

func bgra(r, g, b, a uint8) uint32 {
	// Premultiply alpha for Windows ARGB icons
	pr := uint32(r) * uint32(a) / 255
	pg := uint32(g) * uint32(a) / 255
	pb := uint32(b) * uint32(a) / 255
	return (uint32(a) << 24) | (pr << 16) | (pg << 8) | pb
}
