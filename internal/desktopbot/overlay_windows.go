//go:build windows && (amd64 || arm64)

package desktopbot

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sptask/sptask_desktop_runner/internal/operator"
)

// screenOverlay is a click-through, always-on-top window that shows whose
// work the bot is doing and what it does now. Its thread also owns the
// Shift+Esc hotkey and the low-level mouse and keyboard hooks.
//
// While the bot replays, a physical (not injected) click or key press stops
// the run; the hooks read only the event type and the injected flag.
//
// While a person teaches (record mode) the overlay is a small badge in the
// corner and the hooks tell the recorder about left clicks and their screen
// point, and about Enter, Escape, shortcuts and right clicks. To classify a
// key the hook reads its virtual-key code and the modifiers held; only the
// class (classifyKey) leaves the hook, never the key, so typed text is never
// seen here: values are read from the fields afterwards. Injected input is
// recorded too, so tests can teach; no bot runs during a recording (joblock).

var (
	modGdi32 = windows.NewLazySystemDLL("gdi32.dll")

	procRegisterClassExW           = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW            = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW             = modUser32.NewProc("DefWindowProcW")
	procDestroyWindow              = modUser32.NewProc("DestroyWindow")
	procGetMessageW                = modUser32.NewProc("GetMessageW")
	procTranslateMessage           = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW           = modUser32.NewProc("DispatchMessageW")
	procPostMessageW               = modUser32.NewProc("PostMessageW")
	procPostQuitMessage            = modUser32.NewProc("PostQuitMessage")
	procSetLayeredWindowAttributes = modUser32.NewProc("SetLayeredWindowAttributes")
	procSetWindowPos               = modUser32.NewProc("SetWindowPos")
	procShowWindow                 = modUser32.NewProc("ShowWindow")
	procInvalidateRect             = modUser32.NewProc("InvalidateRect")
	procBeginPaint                 = modUser32.NewProc("BeginPaint")
	procEndPaint                   = modUser32.NewProc("EndPaint")
	procFillRect                   = modUser32.NewProc("FillRect")
	procDrawTextW                  = modUser32.NewProc("DrawTextW")
	procRegisterHotKey             = modUser32.NewProc("RegisterHotKey")
	procUnregisterHotKey           = modUser32.NewProc("UnregisterHotKey")
	procSetWindowsHookExW          = modUser32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx        = modUser32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx             = modUser32.NewProc("CallNextHookEx")
	procSetProcessDpiAwarenessCtx  = modUser32.NewProc("SetProcessDpiAwarenessContext")
	procCreateSolidBrush           = modGdi32.NewProc("CreateSolidBrush")
	procCreatePen                  = modGdi32.NewProc("CreatePen")
	procDeleteObject               = modGdi32.NewProc("DeleteObject")
	procSelectObject               = modGdi32.NewProc("SelectObject")
	procEllipse                    = modGdi32.NewProc("Ellipse")
	procRoundRect                  = modGdi32.NewProc("RoundRect")
	procSetBkMode                  = modGdi32.NewProc("SetBkMode")
	procSetTextColor               = modGdi32.NewProc("SetTextColor")
	procCreateFontW                = modGdi32.NewProc("CreateFontW")
)

const (
	wsPopup           = 0x80000000
	wsExTopmost       = 0x00000008
	wsExTransparent   = 0x00000020
	wsExToolWindow    = 0x00000080
	wsExLayered       = 0x00080000
	wsExNoActivate    = 0x08000000
	lwaColorKey       = 0x1
	swShowNoActivate  = 4
	swpNoActivate     = 0x0010
	swpShowWindow     = 0x0040
	hwndTopmost       = ^uintptr(0) // (HWND)-1
	wmDestroy         = 0x0002
	wmPaint           = 0x000F
	wmClose           = 0x0010
	wmHotkey          = 0x0312
	wmApp             = 0x8000
	wmOverlayMove     = wmApp + 1
	wmKeyDown         = 0x0100
	wmSysKeyDown      = 0x0104
	wmLButtonDown     = 0x0201
	wmRButtonDown     = 0x0204
	llkhfAltDown      = 0x20
	recordMargin      = 16
	wmMButtonDown     = 0x0207
	whKeyboardLL      = 13
	whMouseLL         = 14
	llkhfInjected     = 0x10
	llmhfInjected     = 0x1
	modShift          = 0x0004
	modNoRepeat       = 0x4000
	stopHotkeyID      = 1
	transparentBkMode = 1
	dtLeft            = 0x0
	dtSingleLine      = 0x20
	dtEndEllipsis     = 0x8000
	dtVCenter         = 0x4
	fwBold            = 700
	fwNormal          = 400
	clearTypeQuality  = 5
	psSolid           = 0

	// The key colour is never drawn: Windows makes it see-through.
	keyColor      = 0x00FF00FF
	overlayWidth  = 380
	overlayHeight = 76
	cursorSize    = 22
	pillLeft      = cursorSize + 6
	textColor     = 0x00FFFFFF

	// dpiPerMonitorAware2 is DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2.
	dpiPerMonitorAware2 = ^uintptr(3) // (HANDLE)-4
)

type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   windows.Handle
	icon       windows.Handle
	cursor     windows.Handle
	background windows.Handle
	menuName   *uint16
	className  *uint16
	iconSm     windows.Handle
}

type winMsg struct {
	hwnd    windows.HWND
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
	private uint32
}

type paintStruct struct {
	hdc       windows.Handle
	erase     int32
	paint     windows.Rect
	restore   int32
	incUpdate int32
	reserved  [32]byte
}

type kbdLLHook struct {
	vkCode, scanCode, flags, time uint32
	extra                         uintptr
}

type mouseLLHook struct {
	pt                     struct{ x, y int32 }
	mouseData, flags, time uint32
	extra                  uintptr
}

// Windows callbacks are never freed, so they are made once and dispatch to
// the one overlay that is showing.
var (
	overlayCallbacks sync.Once
	wndProcCB        uintptr
	keyboardHookCB   uintptr
	mouseHookCB      uintptr
	classErr         error
	className        = windows.StringToUTF16Ptr("SpartaskOperatorOverlay")
	activeOverlay    atomic.Pointer[screenOverlay]
	dpiOnce          sync.Once
)

// makeDPIAware makes UI Automation rectangles and the overlay use physical
// pixels on every monitor. It is process wide and set once.
func makeDPIAware() {
	dpiOnce.Do(func() {
		if procSetProcessDpiAwarenessCtx.Find() == nil {
			procSetProcessDpiAwarenessCtx.Call(dpiPerMonitorAware2)
		}
	})
}

type overlayState struct {
	x, y  int32
	label string
}

type screenOverlay struct {
	op     operator.Operator
	hint   string
	onStop func()
	// input is set in record mode; the hooks call it on their thread.
	input func(hookEvent)

	hwnd    windows.HWND
	done    chan struct{}
	stopped atomic.Bool

	mu    sync.Mutex
	state overlayState

	warnMu   sync.Mutex
	warnings []string
}

func registerOverlayClass() error {
	overlayCallbacks.Do(func() {
		wndProcCB = windows.NewCallback(overlayWndProc)
		keyboardHookCB = windows.NewCallback(keyboardHook)
		mouseHookCB = windows.NewCallback(mouseHook)
		var instance windows.Handle
		if err := windows.GetModuleHandleEx(0, nil, &instance); err != nil {
			classErr = fmt.Errorf("module handle: %w", err)
			return
		}
		wc := wndClassEx{wndProc: wndProcCB, instance: instance, className: className}
		wc.size = uint32(unsafe.Sizeof(wc))
		if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
			classErr = fmt.Errorf("register overlay window class: %w", err)
		}
	})
	return classErr
}

// startOverlay shows the overlay and starts watching for the person at the
// computer. onStop runs once, on the overlay thread, when they take over.
func startOverlay(op operator.Operator, onStop func()) (*screenOverlay, error) {
	return openOverlay(&screenOverlay{op: op.Normalized(), hint: operator.StopHint, onStop: onStop})
}

// startRecordingOverlay shows the recording badge with label (the programs
// being recorded) and passes clicks and keys to input. onStop runs once when
// the person presses Shift+Esc.
func startRecordingOverlay(label string, onStop func(), input func(hookEvent)) (*screenOverlay, error) {
	o := &screenOverlay{
		op:     operator.Operator{Name: recordTitle, Color: recordColor}.Normalized(),
		hint:   recordHint,
		onStop: onStop,
		input:  input,
		state:  overlayState{label: label},
	}
	return openOverlay(o)
}

func openOverlay(o *screenOverlay) (*screenOverlay, error) {
	makeDPIAware()
	if err := registerOverlayClass(); err != nil {
		return nil, err
	}
	o.done = make(chan struct{})
	if !activeOverlay.CompareAndSwap(nil, o) {
		return nil, botErr(CodeBusy, "another desktop overlay is showing")
	}
	ready := make(chan error, 1)
	go o.run(ready)
	if err := <-ready; err != nil {
		<-o.done
		activeOverlay.CompareAndSwap(o, nil)
		return nil, err
	}
	return o, nil
}

func (o *screenOverlay) run(ready chan<- error) {
	defer close(o.done)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var instance windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &instance); err != nil {
		ready <- fmt.Errorf("module handle: %w", err)
		return
	}
	hwnd, _, err := procCreateWindowExW.Call(
		wsExLayered|wsExTransparent|wsExTopmost|wsExToolWindow|wsExNoActivate,
		uintptr(unsafe.Pointer(className)), 0, wsPopup,
		0, 0, overlayWidth, overlayHeight, 0, 0, uintptr(instance), 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("create overlay window: %w", err)
		return
	}
	o.hwnd = windows.HWND(hwnd)
	if ok, _, err := procSetLayeredWindowAttributes.Call(hwnd, keyColor, 0, lwaColorKey); ok == 0 {
		procDestroyWindow.Call(hwnd)
		ready <- fmt.Errorf("make overlay see-through: %w", err)
		return
	}
	if ok, _, err := procRegisterHotKey.Call(hwnd, stopHotkeyID, modShift|modNoRepeat, vkEscape); ok == 0 {
		// Another program owns Shift+Esc; a physical key press still stops the run.
		o.warn(fmt.Sprintf("Shift+Esc could not be reserved: %v", err))
	} else {
		defer procUnregisterHotKey.Call(hwnd, stopHotkeyID)
	}
	kb, _, kbErr := procSetWindowsHookExW.Call(whKeyboardLL, keyboardHookCB, uintptr(instance), 0)
	ms, _, msErr := procSetWindowsHookExW.Call(whMouseLL, mouseHookCB, uintptr(instance), 0)
	if kb != 0 {
		defer procUnhookWindowsHookEx.Call(kb)
	}
	if ms != 0 {
		defer procUnhookWindowsHookEx.Call(ms)
	}
	if kb == 0 || ms == 0 {
		// Without the watch nobody could take over by touching the computer.
		procDestroyWindow.Call(hwnd)
		ready <- fmt.Errorf("watch for the person at the computer: %v", errors.Join(hookErr(kb, kbErr), hookErr(ms, msErr)))
		return
	}
	if o.input != nil {
		// The recording badge sits in the bottom-right corner, above the taskbar.
		area, err := workArea()
		if err != nil {
			procDestroyWindow.Call(hwnd)
			ready <- err
			return
		}
		procSetWindowPos.Call(hwnd, hwndTopmost, uintptr(area.Right-overlayWidth-recordMargin), uintptr(area.Bottom-overlayHeight-recordMargin),
			overlayWidth, overlayHeight, swpNoActivate|swpShowWindow)
		procShowWindow.Call(hwnd, swShowNoActivate)
	}
	ready <- nil

	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func hookErr(h uintptr, err error) error {
	if h != 0 {
		return nil
	}
	return err
}

// takeOver is called when the person at the computer stops the bot.
func (o *screenOverlay) takeOver() {
	if o.stopped.CompareAndSwap(false, true) {
		o.onStop()
	}
}

func (o *screenOverlay) warn(msg string) {
	o.warnMu.Lock()
	defer o.warnMu.Unlock()
	o.warnings = append(o.warnings, msg)
}

func (o *screenOverlay) takeWarnings() []string {
	o.warnMu.Lock()
	defer o.warnMu.Unlock()
	w := o.warnings
	o.warnings = nil
	return w
}

// pointAt moves the cursor to (x, y) in screen pixels and shows label.
func (o *screenOverlay) pointAt(x, y int32, label string) {
	o.mu.Lock()
	o.state = overlayState{x: x, y: y, label: label}
	o.mu.Unlock()
	procPostMessageW.Call(uintptr(o.hwnd), wmOverlayMove, 0, 0)
}

// close removes the overlay, the hotkey and the hooks and waits for its thread.
func (o *screenOverlay) close() {
	procPostMessageW.Call(uintptr(o.hwnd), wmClose, 0, 0)
	<-o.done
	activeOverlay.CompareAndSwap(o, nil)
}

func overlayWndProc(hwnd windows.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	o := activeOverlay.Load()
	if o == nil || o.hwnd != hwnd {
		r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
		return r
	}
	switch msg {
	case wmOverlayMove:
		o.mu.Lock()
		s := o.state
		o.mu.Unlock()
		half := int32(cursorSize / 2)
		procSetWindowPos.Call(uintptr(hwnd), hwndTopmost, uintptr(s.x-half), uintptr(s.y-half),
			overlayWidth, overlayHeight, swpNoActivate|swpShowWindow)
		procShowWindow.Call(uintptr(hwnd), swShowNoActivate)
		procInvalidateRect.Call(uintptr(hwnd), 0, 1)
		return 0
	case wmPaint:
		o.paint(hwnd)
		return 0
	case wmHotkey:
		if wParam == stopHotkeyID {
			o.takeOver()
		}
		return 0
	case wmClose:
		procDestroyWindow.Call(uintptr(hwnd))
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

func keyboardHook(code int32, wParam uintptr, lParam unsafe.Pointer) uintptr {
	if code >= 0 && (wParam == wmKeyDown || wParam == wmSysKeyDown) {
		ev := (*kbdLLHook)(lParam)
		if o := activeOverlay.Load(); o != nil {
			if o.input != nil {
				o.recordKey(ev)
			} else if ev.flags&llkhfInjected == 0 {
				o.takeOver()
			}
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, uintptr(code), wParam, uintptr(lParam))
	return r
}

func mouseHook(code int32, wParam uintptr, lParam unsafe.Pointer) uintptr {
	if code >= 0 && (wParam == wmLButtonDown || wParam == wmRButtonDown || wParam == wmMButtonDown) {
		ev := (*mouseLLHook)(lParam)
		if o := activeOverlay.Load(); o != nil {
			if o.input != nil {
				o.recordMouse(wParam, ev)
			} else if ev.flags&llmhfInjected == 0 {
				o.takeOver()
			}
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, uintptr(code), wParam, uintptr(lParam))
	return r
}

// hookKind is what a recording hook saw.
type hookKind int

const (
	hookClick hookKind = iota
	hookKey
	hookRightClick
	hookShortcut
)

// hookEvent goes from the hooks to the recorder: a click and its screen
// point, or the class of a key (Enter, Escape, a shortcut), never the key.
type hookEvent struct {
	kind hookKind
	x, y int32
	key  keyClass
}

func (o *screenOverlay) recordKey(ev *kbdLLHook) {
	switch c := classifyKey(ev.vkCode, keyHeld(vkShift), keyHeld(vkControl), ev.flags&llkhfAltDown != 0); c {
	case keyStop:
		o.takeOver()
	case keyEnter, keyEscape:
		o.input(hookEvent{kind: hookKey, key: c})
	case keyShortcut:
		o.input(hookEvent{kind: hookShortcut})
	}
}

func (o *screenOverlay) recordMouse(wParam uintptr, ev *mouseLLHook) {
	switch wParam {
	case wmLButtonDown:
		o.input(hookEvent{kind: hookClick, x: ev.pt.x, y: ev.pt.y})
	case wmRButtonDown:
		o.input(hookEvent{kind: hookRightClick, x: ev.pt.x, y: ev.pt.y})
	}
}

// colorRef turns #rrggbb into a Windows COLORREF (0x00bbggrr). Operator
// colours are normalized before they get here; anything else draws in
// SparTask's accent (operator.DefaultColor).
func colorRef(hex string) uintptr {
	const defaultColorRef = 0x00ED3A7C
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil || v > 0xFFFFFF {
		return defaultColorRef
	}
	r, g, b := (v>>16)&0xff, (v>>8)&0xff, v&0xff
	return uintptr(b<<16 | g<<8 | r)
}

func (o *screenOverlay) paint(hwnd windows.HWND) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
	o.mu.Lock()
	label := o.state.label
	o.mu.Unlock()

	all := windows.Rect{Right: overlayWidth, Bottom: overlayHeight}
	key, _, _ := procCreateSolidBrush.Call(keyColor)
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&all)), key)
	procDeleteObject.Call(key)

	color := colorRef(o.op.Color)
	brush, _, _ := procCreateSolidBrush.Call(color)
	defer procDeleteObject.Call(brush)
	pen, _, _ := procCreatePen.Call(psSolid, 2, textColor)
	defer procDeleteObject.Call(pen)
	oldBrush, _, _ := procSelectObject.Call(hdc, brush)
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	// The replay overlay is a cursor dot with the pill beside it; the
	// recording badge is the pill alone.
	left, top := int32(pillLeft), int32(cursorSize/2)
	if o.input != nil {
		left, top = 1, 1
	} else {
		procEllipse.Call(hdc, 1, 1, cursorSize, cursorSize)
	}
	procRoundRect.Call(hdc, uintptr(left), uintptr(top), overlayWidth-1, overlayHeight-1, 14, 14)
	procSelectObject.Call(hdc, oldPen)
	procSelectObject.Call(hdc, oldBrush)

	procSetBkMode.Call(hdc, transparentBkMode)
	procSetTextColor.Call(hdc, textColor)
	lines := []struct {
		text   string
		height int32
		weight uintptr
		top    int32
	}{
		{o.op.Name, 18, fwBold, top + 4},
		{label, 16, fwNormal, top + 23},
		{o.hint, 13, fwNormal, top + 41},
	}
	face := windows.StringToUTF16Ptr("Segoe UI")
	for _, l := range lines {
		if l.text == "" {
			continue
		}
		font, _, _ := procCreateFontW.Call(uintptr(-l.height), 0, 0, 0, l.weight, 0, 0, 0, 0, 0, 0, clearTypeQuality, 0, uintptr(unsafe.Pointer(face)))
		oldFont, _, _ := procSelectObject.Call(hdc, font)
		r := windows.Rect{Left: left + 12, Top: l.top, Right: overlayWidth - 10, Bottom: l.top + l.height + 2}
		text := windows.StringToUTF16Ptr(l.text)
		procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(text)), ^uintptr(0), uintptr(unsafe.Pointer(&r)), dtLeft|dtSingleLine|dtVCenter|dtEndEllipsis)
		procSelectObject.Call(hdc, oldFont)
		procDeleteObject.Call(font)
	}
}
