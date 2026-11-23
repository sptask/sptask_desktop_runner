//go:build windows && (amd64 || arm64)

package desktopbot

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// UI Automation through its COM interfaces, without CGO. The vtable slot of
// every method used here is listed next to it; uia_integration_test.go checks
// each one against a fixture window whose answers are known, so a wrong slot
// fails a test instead of misbehaving on a user's machine.

var (
	modOle32             = windows.NewLazySystemDLL("ole32.dll")
	modOleaut32          = windows.NewLazySystemDLL("oleaut32.dll")
	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")
	procSysAllocString   = modOleaut32.NewProc("SysAllocString")
	procSysFreeString    = modOleaut32.NewProc("SysFreeString")

	clsidCUIAutomation = windows.GUID{Data1: 0xff48dba4, Data2: 0x60ef, Data3: 0x4201, Data4: [8]byte{0xaa, 0x87, 0x54, 0x10, 0x3e, 0xef, 0x59, 0x4e}}
	iidIUIAutomation   = windows.GUID{Data1: 0x30cbe57d, Data2: 0xd9d0, Data3: 0x452a, Data4: [8]byte{0xab, 0x13, 0x7a, 0xc5, 0xac, 0x48, 0x25, 0xee}}

	iidInvokePattern         = windows.GUID{Data1: 0xfb377fbe, Data2: 0x8ea6, Data3: 0x46d5, Data4: [8]byte{0x9c, 0x73, 0x64, 0x99, 0x64, 0x2d, 0x30, 0x59}}
	iidValuePattern          = windows.GUID{Data1: 0xa94cd8b1, Data2: 0x0844, Data3: 0x4cd6, Data4: [8]byte{0x9d, 0x2d, 0x64, 0x05, 0x37, 0xab, 0x39, 0xe9}}
	iidTogglePattern         = windows.GUID{Data1: 0x94cf8058, Data2: 0x9b8d, Data3: 0x4ab9, Data4: [8]byte{0x8b, 0xfd, 0x4c, 0xd0, 0xa3, 0x3c, 0x8c, 0x70}}
	iidSelectionItemPattern  = windows.GUID{Data1: 0xa8efa66a, Data2: 0x0fda, Data3: 0x421a, Data4: [8]byte{0x91, 0x94, 0x38, 0x02, 0x1f, 0x35, 0x78, 0xea}}
	iidExpandCollapsePattern = windows.GUID{Data1: 0x619be086, Data2: 0x1f4e, Data3: 0x4ee4, Data4: [8]byte{0xba, 0xfa, 0x21, 0x01, 0x28, 0x73, 0x87, 0x30}}
)

// Pattern ids (UIAutomationClient.h, UIA_*PatternId).
const (
	patternInvoke         = 10000
	patternValue          = 10002
	patternExpandCollapse = 10005
	patternSelectionItem  = 10010
	patternToggle         = 10015
)

const (
	clsctxInprocServer = 0x1
	hrNoInterface      = 0x80004002
)

// Vtable slots. IUnknown takes 0-2.
const (
	slotAddRef  = 1
	slotRelease = 2

	// IUIAutomation
	slotCompareElements      = 3
	slotElementFromHandle    = 6
	slotElementFromPoint     = 7
	slotGetFocusedElement    = 8
	slotGetControlViewWalker = 14

	// IUIAutomationTreeWalker
	slotWalkerParent      = 3
	slotWalkerFirstChild  = 4
	slotWalkerNextSibling = 6

	// IUIAutomationElement
	slotSetFocus                = 3
	slotGetCurrentPatternAs     = 14
	slotCurrentProcessID        = 20
	slotCurrentControlType      = 21
	slotCurrentName             = 23
	slotCurrentIsEnabled        = 28
	slotCurrentAutomationID     = 29
	slotCurrentClassName        = 30
	slotCurrentIsPassword       = 35
	slotCurrentNativeWindow     = 36
	slotCurrentIsOffscreen      = 38
	slotCurrentBoundingRectangl = 43

	// Patterns: the first method after IUnknown.
	slotPatternAction        = 3 // Invoke, SetValue, Toggle, Select, Expand
	slotValueCurrentValue    = 4
	slotValueIsReadOnly      = 5
	slotExpandCollapseCollap = 4
	slotExpandCollapseState  = 5
)

// expandCollapseExpanded is ExpandCollapseState_Expanded.
const expandCollapseExpanded = 1

// comObj is one COM interface pointer.
type comObj struct{ ptr unsafe.Pointer }

func (o comObj) valid() bool { return o.ptr != nil }

func (o comObj) call(slot int, args ...uintptr) error {
	vtbl := *(*unsafe.Pointer)(o.ptr)
	fn := *(*uintptr)(unsafe.Add(vtbl, slot*int(unsafe.Sizeof(uintptr(0)))))
	all := append([]uintptr{uintptr(o.ptr)}, args...)
	hr, _, _ := syscall.SyscallN(fn, all...)
	if int32(hr) < 0 {
		return hresultError(hr)
	}
	return nil
}

// addRef returns a second reference to the same object; each one is released
// once.
func (o comObj) addRef() comObj {
	if o.ptr == nil {
		return comObj{}
	}
	vtbl := *(*unsafe.Pointer)(o.ptr)
	fn := *(*uintptr)(unsafe.Add(vtbl, slotAddRef*int(unsafe.Sizeof(uintptr(0)))))
	syscall.SyscallN(fn, uintptr(o.ptr))
	return comObj{o.ptr}
}

func (o *comObj) release() {
	if o.ptr == nil {
		return
	}
	vtbl := *(*unsafe.Pointer)(o.ptr)
	fn := *(*uintptr)(unsafe.Add(vtbl, slotRelease*int(unsafe.Sizeof(uintptr(0)))))
	syscall.SyscallN(fn, uintptr(o.ptr))
	o.ptr = nil
}

type hresultError uintptr

func (e hresultError) Error() string { return fmt.Sprintf("HRESULT 0x%08X", uint32(e)) }

func isNoInterface(err error) bool {
	var hr hresultError
	return errors.As(err, &hr) && uint32(hr) == hrNoInterface
}

func newBSTR(s string) (uintptr, error) {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return 0, fmt.Errorf("encode text: %w", err)
	}
	b, _, _ := procSysAllocString.Call(uintptr(unsafe.Pointer(p)))
	if b == 0 {
		return 0, errors.New("out of memory for text")
	}
	return b, nil
}

func freeBSTR(b uintptr) {
	if b != 0 {
		procSysFreeString.Call(b)
	}
}

// readBSTR takes a BSTR that a COM method returned, copies it and frees it.
func readBSTR(b *uint16) string {
	if b == nil {
		return ""
	}
	defer procSysFreeString.Call(uintptr(unsafe.Pointer(b)))
	return windows.UTF16PtrToString(b)
}

// uiaThread owns the UI Automation client. Every COM call runs on its single
// locked OS thread, which joined the multithreaded apartment.
type uiaThread struct {
	auto   comObj
	walker comObj
	work   chan func()
	done   chan struct{}
}

func startUIA() (*uiaThread, error) {
	u := &uiaThread{work: make(chan func()), done: make(chan struct{})}
	ready := make(chan error, 1)
	go func() {
		defer close(u.done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil {
			ready <- fmt.Errorf("start COM: %w", err)
			return
		}
		defer windows.CoUninitialize()
		var auto unsafe.Pointer
		hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidCUIAutomation)), 0, clsctxInprocServer,
			uintptr(unsafe.Pointer(&iidIUIAutomation)), uintptr(unsafe.Pointer(&auto)))
		if int32(hr) < 0 {
			ready <- fmt.Errorf("create UI Automation: %w", hresultError(hr))
			return
		}
		u.auto = comObj{auto}
		defer u.auto.release()
		var walker unsafe.Pointer
		if err := u.auto.call(slotGetControlViewWalker, uintptr(unsafe.Pointer(&walker))); err != nil {
			ready <- fmt.Errorf("get control view walker: %w", err)
			return
		}
		u.walker = comObj{walker}
		defer u.walker.release()
		ready <- nil
		for fn := range u.work {
			fn()
		}
	}()
	if err := <-ready; err != nil {
		<-u.done
		return nil, err
	}
	return u, nil
}

// do runs fn on the UI Automation thread and waits for it.
func (u *uiaThread) do(fn func() error) error {
	errc := make(chan error, 1)
	u.work <- func() { errc <- fn() }
	return <-errc
}

// close ends the thread after the calls in flight; it releases the client.
func (u *uiaThread) close() {
	close(u.work)
	<-u.done
}

// element is an IUIAutomationElement. Every reference is used by one UIA
// thread at a time and released exactly once: the recorder hands its selector
// thread a reference of its own (addRef), which that thread releases.
type element struct{ comObj }

func (e element) addRef() element { return element{e.comObj.addRef()} }

func (u *uiaThread) elementFromHandle(hwnd windows.HWND) (element, error) {
	var out unsafe.Pointer
	if err := u.auto.call(slotElementFromHandle, uintptr(hwnd), uintptr(unsafe.Pointer(&out))); err != nil {
		return element{}, fmt.Errorf("element of window: %w", err)
	}
	if out == nil {
		return element{}, errors.New("window has no UI Automation element")
	}
	return element{comObj{out}}, nil
}

// elementFromPoint returns the control at a screen point (physical pixels).
// POINT is passed by value: on 64-bit Windows its two int32 fields travel
// packed in one register, x in the low half.
func (u *uiaThread) elementFromPoint(x, y int32) (element, error) {
	var out unsafe.Pointer
	pt := uintptr(uint32(x)) | uintptr(uint32(y))<<32
	if err := u.auto.call(slotElementFromPoint, pt, uintptr(unsafe.Pointer(&out))); err != nil {
		return element{}, fmt.Errorf("element at point: %w", err)
	}
	return element{comObj{out}}, nil
}

// focusedElement returns the control that has the keyboard focus.
func (u *uiaThread) focusedElement() (element, error) {
	var out unsafe.Pointer
	if err := u.auto.call(slotGetFocusedElement, uintptr(unsafe.Pointer(&out))); err != nil {
		return element{}, fmt.Errorf("focused element: %w", err)
	}
	return element{comObj{out}}, nil
}

// sameElement reports whether two references are the same control.
func (u *uiaThread) sameElement(a, b element) (bool, error) {
	var same int32
	if err := u.auto.call(slotCompareElements, uintptr(a.ptr), uintptr(b.ptr), uintptr(unsafe.Pointer(&same))); err != nil {
		return false, fmt.Errorf("compare elements: %w", err)
	}
	return same != 0, nil
}

func (u *uiaThread) walk(slot int, from element) (element, error) {
	var out unsafe.Pointer
	if err := u.walker.call(slot, uintptr(from.ptr), uintptr(unsafe.Pointer(&out))); err != nil {
		return element{}, err
	}
	return element{comObj{out}}, nil // nil when there is none
}

func (u *uiaThread) firstChild(e element) (element, error)  { return u.walk(slotWalkerFirstChild, e) }
func (u *uiaThread) nextSibling(e element) (element, error) { return u.walk(slotWalkerNextSibling, e) }
func (u *uiaThread) parent(e element) (element, error)      { return u.walk(slotWalkerParent, e) }

func (e element) bstrProp(slot int) (string, error) {
	var b *uint16
	if err := e.call(slot, uintptr(unsafe.Pointer(&b))); err != nil {
		return "", err
	}
	return readBSTR(b), nil
}

func (e element) intProp(slot int) (int32, error) {
	var v int32
	err := e.call(slot, uintptr(unsafe.Pointer(&v)))
	return v, err
}

func (e element) name() (string, error)         { return e.bstrProp(slotCurrentName) }
func (e element) automationID() (string, error) { return e.bstrProp(slotCurrentAutomationID) }
func (e element) className() (string, error)    { return e.bstrProp(slotCurrentClassName) }
func (e element) processID() (uint32, error) {
	v, err := e.intProp(slotCurrentProcessID)
	return uint32(v), err
}
func (e element) controlType() (int32, error) { return e.intProp(slotCurrentControlType) }
func (e element) isEnabled() (bool, error) {
	v, err := e.intProp(slotCurrentIsEnabled)
	return v != 0, err
}
func (e element) isPassword() (bool, error) {
	v, err := e.intProp(slotCurrentIsPassword)
	return v != 0, err
}
func (e element) isOffscreen() (bool, error) {
	v, err := e.intProp(slotCurrentIsOffscreen)
	return v != 0, err
}

func (e element) nativeWindow() (windows.HWND, error) {
	var h uintptr
	err := e.call(slotCurrentNativeWindow, uintptr(unsafe.Pointer(&h)))
	return windows.HWND(h), err
}

func (e element) bounds() (windows.Rect, error) {
	var r windows.Rect
	err := e.call(slotCurrentBoundingRectangl, uintptr(unsafe.Pointer(&r)))
	return r, err
}

func (e element) setFocus() error { return e.call(slotSetFocus) }

// pattern returns the control pattern, or an invalid comObj when the control
// does not support it.
func (e element) pattern(id int, iid *windows.GUID) (comObj, error) {
	var out unsafe.Pointer
	err := e.call(slotGetCurrentPatternAs, uintptr(id), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if isNoInterface(err) {
		return comObj{}, nil
	}
	if err != nil {
		return comObj{}, err
	}
	return comObj{out}, nil
}

// valueOf reads ValuePattern.CurrentValue; ok is false without the pattern.
func (e element) valueOf() (value string, ok bool, err error) {
	vp, err := e.pattern(patternValue, &iidValuePattern)
	if err != nil || !vp.valid() {
		return "", false, err
	}
	defer vp.release()
	var b *uint16
	if err := vp.call(slotValueCurrentValue, uintptr(unsafe.Pointer(&b))); err != nil {
		return "", true, fmt.Errorf("read value: %w", err)
	}
	return readBSTR(b), true, nil
}

// valueState reads ValuePattern's value and whether it is read-only; ok is
// false without the pattern. The caller never asks a password field.
func (e element) valueState() (value string, readOnly, ok bool, err error) {
	vp, err := e.pattern(patternValue, &iidValuePattern)
	if err != nil || !vp.valid() {
		return "", false, false, err
	}
	defer vp.release()
	var ro int32
	if err := vp.call(slotValueIsReadOnly, uintptr(unsafe.Pointer(&ro))); err != nil {
		return "", false, true, fmt.Errorf("read value state: %w", err)
	}
	var b *uint16
	if err := vp.call(slotValueCurrentValue, uintptr(unsafe.Pointer(&b))); err != nil {
		return "", ro != 0, true, fmt.Errorf("read value: %w", err)
	}
	return readBSTR(b), ro != 0, true, nil
}

// expanded reports whether a drop-down's list is open.
func (e element) expanded() (bool, error) {
	p, err := e.pattern(patternExpandCollapse, &iidExpandCollapsePattern)
	if err != nil || !p.valid() {
		return false, err
	}
	defer p.release()
	var state int32
	if err := p.call(slotExpandCollapseState, uintptr(unsafe.Pointer(&state))); err != nil {
		return false, fmt.Errorf("read expand state: %w", err)
	}
	return state == expandCollapseExpanded, nil
}

// setValue writes through ValuePattern; ok is false when the control has no
// writable value.
func (e element) setValue(value string) (ok bool, err error) {
	vp, err := e.pattern(patternValue, &iidValuePattern)
	if err != nil || !vp.valid() {
		return false, err
	}
	defer vp.release()
	var readOnly int32
	if err := vp.call(slotValueIsReadOnly, uintptr(unsafe.Pointer(&readOnly))); err != nil {
		return false, fmt.Errorf("read value state: %w", err)
	}
	if readOnly != 0 {
		return false, nil
	}
	b, err := newBSTR(value)
	if err != nil {
		return false, err
	}
	defer freeBSTR(b)
	if err := vp.call(slotPatternAction, b); err != nil {
		return true, fmt.Errorf("set value: %w", err)
	}
	return true, nil
}

// act runs the pattern's first method (Invoke, Toggle, Select, Expand);
// ok is false when the control does not support the pattern.
func (e element) act(id int, iid *windows.GUID) (ok bool, err error) {
	p, err := e.pattern(id, iid)
	if err != nil || !p.valid() {
		return false, err
	}
	defer p.release()
	return true, p.call(slotPatternAction)
}

func (e element) collapse() error {
	p, err := e.pattern(patternExpandCollapse, &iidExpandCollapsePattern)
	if err != nil || !p.valid() {
		return err
	}
	defer p.release()
	return p.call(slotExpandCollapseCollap)
}

// Control type ids (UIA_*ControlTypeId) and the names recordings use.
var controlTypeNames = map[int32]string{
	50000: "Button", 50001: "Calendar", 50002: "CheckBox", 50003: "ComboBox", 50004: "Edit",
	50005: "Hyperlink", 50006: "Image", 50007: "ListItem", 50008: "List", 50009: "Menu",
	50010: "MenuBar", 50011: "MenuItem", 50012: "ProgressBar", 50013: "RadioButton", 50014: "ScrollBar",
	50015: "Slider", 50016: "Spinner", 50017: "StatusBar", 50018: "Tab", 50019: "TabItem",
	50020: "Text", 50021: "ToolBar", 50022: "ToolTip", 50023: "Tree", 50024: "TreeItem",
	50025: "Custom", 50026: "Group", 50027: "Thumb", 50028: "DataGrid", 50029: "DataItem",
	50030: "Document", 50031: "SplitButton", 50032: "Window", 50033: "Pane", 50034: "Header",
	50035: "HeaderItem", 50036: "Table", 50037: "TitleBar", 50038: "Separator",
}

func controlTypeName(id int32) string {
	if n, ok := controlTypeNames[id]; ok {
		return n
	}
	return fmt.Sprintf("Type%d", id)
}
