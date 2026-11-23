//go:build windows && (amd64 || arm64)

package desktopbot

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32                = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows          = modUser32.NewProc("EnumWindows")
	procIsWindowVisible      = modUser32.NewProc("IsWindowVisible")
	procGetWindowTextW       = modUser32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW = modUser32.NewProc("GetWindowTextLengthW")
	procSetForegroundWindow  = modUser32.NewProc("SetForegroundWindow")
	procOpenInputDesktop     = modUser32.NewProc("OpenInputDesktop")
	procCloseDesktop         = modUser32.NewProc("CloseDesktop")
	procSendInput            = modUser32.NewProc("SendInput")
	procIsWindow             = modUser32.NewProc("IsWindow")
	procGetAncestor          = modUser32.NewProc("GetAncestor")
	procSendMessageTimeoutW  = modUser32.NewProc("SendMessageTimeoutW")
)

const (
	gaRoot              = 2
	desktopReadObjects  = 0x0001
	inputKeyboard       = 1
	keyeventfKeyUp      = 0x0002
	maxWindowTitleChars = 512
	cbGetDroppedState   = 0x0157
	smtoAbortIfHung     = 0x0002
	// comboAskTimeoutMS bounds a question to a program that may be busy.
	comboAskTimeoutMS = 200
)

// comboDropped asks a native combo box whether its list is open.
func comboDropped(h windows.HWND) (bool, error) {
	var res uintptr
	ok, _, err := procSendMessageTimeoutW.Call(uintptr(h), cbGetDroppedState, 0, 0, smtoAbortIfHung, comboAskTimeoutMS, uintptr(unsafe.Pointer(&res)))
	if ok == 0 {
		return false, fmt.Errorf("ask the drop-down whether its list is open: %w", err)
	}
	return res != 0, nil
}

// topWindow is a visible top-level window and the program behind it.
type topWindow struct {
	hwnd  windows.HWND
	pid   uint32
	title string
	exe   string // full path; empty when it could not be read
}

// The callback is made once: Windows callbacks made with NewCallback are
// never freed.
var (
	enumOnce     sync.Once
	enumCallback uintptr
	enumMu       sync.Mutex
	enumFound    []windows.HWND
)

func listTopWindows() ([]topWindow, error) {
	enumOnce.Do(func() {
		enumCallback = windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
			if visible, _, _ := procIsWindowVisible.Call(uintptr(hwnd)); visible != 0 {
				enumFound = append(enumFound, hwnd)
			}
			return 1
		})
	})
	enumMu.Lock()
	enumFound = enumFound[:0]
	ok, _, callErr := procEnumWindows.Call(enumCallback, 0)
	handles := append([]windows.HWND(nil), enumFound...)
	enumMu.Unlock()
	if ok == 0 {
		return nil, fmt.Errorf("list windows: %w", callErr)
	}
	out := make([]topWindow, 0, len(handles))
	for _, h := range handles {
		var pid uint32
		if _, err := windows.GetWindowThreadProcessId(h, &pid); err != nil || pid == 0 {
			continue
		}
		out = append(out, topWindow{hwnd: h, pid: pid, title: windowTitle(h), exe: processPath(pid)})
	}
	return out, nil
}

func windowTitle(h windows.HWND) string {
	n, _, _ := procGetWindowTextLengthW.Call(uintptr(h))
	if n == 0 {
		return ""
	}
	if n > maxWindowTitleChars {
		n = maxWindowTitleChars
	}
	buf := make([]uint16, n+1)
	procGetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf)
}

// processPath is the program file of a process, or "" when Windows does not
// tell (the policy then refuses the window).
func processPath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}

// processElevated reports whether a process runs as administrator. A process
// whose token cannot be read counts as elevated: the bot fails closed.
func processElevated(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	var token windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token); err != nil {
		return true
	}
	defer token.Close()
	return token.IsElevated()
}

func windowAlive(h windows.HWND) bool {
	ok, _, _ := procIsWindow.Call(uintptr(h))
	return ok != 0
}

// foregroundPID is the process of the window that receives the keyboard.
func foregroundPID() uint32 {
	h := rootWindow(windows.GetForegroundWindow())
	if h == 0 {
		return 0
	}
	return windowPID(h)
}

func bringToFront(h windows.HWND) {
	procSetForegroundWindow.Call(uintptr(h))
}

// workstationLocked: the input desktop cannot be opened while the lock screen
// or a secure desktop (UAC) is shown.
func workstationLocked() bool {
	d, _, _ := procOpenInputDesktop.Call(0, 0, desktopReadObjects)
	if d == 0 {
		return true
	}
	procCloseDesktop.Call(d)
	return false
}

// keyboardInput mirrors the Windows INPUT structure for keyboard events.
type keyboardInput struct {
	inputType uint32
	_         uint32 // alignment of the union on 64-bit Windows
	vk        uint16
	scan      uint16
	flags     uint32
	time      uint32
	extra     uintptr
	_         [8]byte // the union is as large as MOUSEINPUT
}

var virtualKeys = map[string]uint16{"Enter": 0x0D, "Tab": 0x09, "Escape": 0x1B}

// sendKey presses and releases one allowed key. The caller has checked that
// the target program has the keyboard.
func sendKey(name string) error {
	vk, ok := virtualKeys[name]
	if !ok {
		return fmt.Errorf("key %q is not allowed", name)
	}
	in := []keyboardInput{
		{inputType: inputKeyboard, vk: vk},
		{inputType: inputKeyboard, vk: vk, flags: keyeventfKeyUp},
	}
	n, _, err := procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0]))
	if int(n) != len(in) {
		return fmt.Errorf("send key %s: %w", name, err)
	}
	return nil
}

// findTopWindow returns the window of a step: the program name must match
// and, when set, the title must contain the step's title.
func findTopWindow(w Window) (topWindow, bool, error) {
	list, err := listTopWindows()
	if err != nil {
		return topWindow{}, false, err
	}
	app := strings.ToLower(w.App)
	title := strings.ToLower(w.Title)
	for _, tw := range list {
		if tw.exe == "" || !strings.EqualFold(baseName(tw.exe), app) {
			continue
		}
		if title != "" && !strings.Contains(strings.ToLower(tw.title), title) {
			continue
		}
		return tw, true, nil
	}
	return topWindow{}, false, nil
}

func baseName(path string) string {
	if i := strings.LastIndexAny(path, `\/`); i >= 0 {
		return path[i+1:]
	}
	return path
}

var errNoElement = errors.New("element not found")

// INPUT is 40 bytes on 64-bit Windows; a different size would make SendInput
// read garbage, so the build fails instead.
var (
	_ [40 - unsafe.Sizeof(keyboardInput{})]byte
	_ [unsafe.Sizeof(keyboardInput{}) - 40]byte
)

// isPopupWindow reports whether a control is a top-level window of its own,
// like the list of an open drop-down.
func isPopupWindow(e element) (bool, error) {
	h, err := e.nativeWindow()
	if err != nil || h == 0 {
		return false, err
	}
	root, _, _ := procGetAncestor.Call(uintptr(h), gaRoot)
	return windows.HWND(root) == h, nil
}

var (
	procWindowFromPoint       = modUser32.NewProc("WindowFromPoint")
	procGetDoubleClickTime    = modUser32.NewProc("GetDoubleClickTime")
	procGetAsyncKeyState      = modUser32.NewProc("GetAsyncKeyState")
	procSystemParametersInfoW = modUser32.NewProc("SystemParametersInfoW")
)

const (
	spiGetWorkArea = 0x0030
	keyDownBit     = 0x8000
)

// rootWindow is the top-level window that h belongs to.
func rootWindow(h windows.HWND) windows.HWND {
	if h == 0 {
		return 0
	}
	root, _, _ := procGetAncestor.Call(uintptr(h), gaRoot)
	if root == 0 {
		return h
	}
	return windows.HWND(root)
}

// windowAt is the top-level window under a screen point. POINT travels by
// value, packed in one register on 64-bit Windows.
func windowAt(x, y int32) windows.HWND {
	h, _, _ := procWindowFromPoint.Call(uintptr(uint32(x)) | uintptr(uint32(y))<<32)
	return rootWindow(windows.HWND(h))
}

func windowPID(h windows.HWND) uint32 {
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(h, &pid); err != nil {
		return 0
	}
	return pid
}

func doubleClickTime() time.Duration {
	ms, _, _ := procGetDoubleClickTime.Call()
	return time.Duration(ms) * time.Millisecond
}

// keyHeld reports whether a modifier key is down right now.
func keyHeld(vk int) bool {
	s, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return s&keyDownBit != 0
}

// workArea is the primary screen without the taskbar.
func workArea() (windows.Rect, error) {
	var r windows.Rect
	if ok, _, err := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0); ok == 0 {
		return windows.Rect{}, fmt.Errorf("read the screen's work area: %w", err)
	}
	return r, nil
}
