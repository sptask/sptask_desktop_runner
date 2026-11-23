//go:build windows

// Command teach uses the fixture form (../fixture) the way a person does, with
// mouse and keyboard input, so an end-to-end test can teach a desktop
// recording. It knows controls by their Win32 control id and acts in order:
//
//	teach -title "SparTask Eczane Fikstürü (E2E)" click:101 type:Ayşe item:104:1 click:201 key:Enter stop
//
//	click:<id>          click the middle of control <id>
//	item:<id>:<index>   click item <index> of the open drop-down <id>
//	type:<text>         type <text> into the focused control
//	key:<name>          press Enter, Escape or Tab
//	stop                press Shift+Esc (ends a recording)
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	inputMouse      = 0
	inputKeyboard   = 1
	keyeventfKeyUp  = 0x0002
	keyeventfUnicod = 0x0004
	mouseLeftDown   = 0x0002
	mouseLeftUp     = 0x0004
	lbGetItemRect   = 0x0198
	swpNoMove       = 0x0002
	swpNoSize       = 0x0001
	swpShowWindow   = 0x0040
	hwndTopmost     = ^uintptr(0)
	vkShift         = 0x10
	vkEscape        = 0x1B
	// dpiPerMonitorAware2 makes window rectangles and the cursor use the
	// same physical pixels as UI Automation in the runner.
	dpiPerMonitorAware2 = ^uintptr(3)
	humanPause          = 400 * time.Millisecond
	findTimeout         = 15 * time.Second
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	findWindowW      = user32.NewProc("FindWindowW")
	getDlgItem       = user32.NewProc("GetDlgItem")
	getWindowRect    = user32.NewProc("GetWindowRect")
	setCursorPos     = user32.NewProc("SetCursorPos")
	sendInput        = user32.NewProc("SendInput")
	sendMessageW     = user32.NewProc("SendMessageW")
	clientToScreen   = user32.NewProc("ClientToScreen")
	getComboBoxInfo  = user32.NewProc("GetComboBoxInfo")
	setWindowPos     = user32.NewProc("SetWindowPos")
	setDpiAwareness  = user32.NewProc("SetProcessDpiAwarenessContext")
	keyNames         = map[string]uint16{"Enter": 0x0D, "Escape": 0x1B, "Tab": 0x09}
	errUnknownAction = fmt.Errorf("unknown action")
)

type rect struct{ left, top, right, bottom int32 }

type comboBoxInfo struct {
	size        uint32
	item        rect
	button      rect
	buttonState uint32
	combo       windows.HWND
	edit        windows.HWND
	list        windows.HWND
}

type mouseInput struct {
	inputType uint32
	_         uint32
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	_         uint32
	extra     uintptr
}

type keyboardInput struct {
	inputType uint32
	_         uint32
	vk        uint16
	scan      uint16
	flags     uint32
	time      uint32
	extra     uintptr
	_         [8]byte
}

func main() {
	title := flag.String("title", "", "title of the fixture window")
	flag.Parse()
	if *title == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: teach -title <window title> action...")
		os.Exit(2)
	}
	if err := run(*title, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(title string, actions []string) error {
	if setDpiAwareness.Find() == nil {
		setDpiAwareness.Call(dpiPerMonitorAware2)
	}
	form, err := findForm(title)
	if err != nil {
		return err
	}
	// Other windows (a test browser) may cover the form; a person would
	// bring it forward. The first click then activates it.
	setWindowPos.Call(uintptr(form), hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow)
	for _, a := range actions {
		if err := act(form, a); err != nil {
			return fmt.Errorf("%s: %w", a, err)
		}
		time.Sleep(humanPause)
	}
	return nil
}

func findForm(title string) (windows.HWND, error) {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return 0, err
	}
	deadline := time.Now().Add(findTimeout)
	for {
		h, _, _ := findWindowW.Call(0, uintptr(unsafe.Pointer(t)))
		if h != 0 {
			return windows.HWND(h), nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("no window titled %q", title)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func act(form windows.HWND, action string) error {
	verb, arg, _ := strings.Cut(action, ":")
	switch verb {
	case "click":
		ctl, err := control(form, arg)
		if err != nil {
			return err
		}
		var r rect
		getWindowRect.Call(uintptr(ctl), uintptr(unsafe.Pointer(&r)))
		return click((r.left+r.right)/2, (r.top+r.bottom)/2)
	case "item":
		id, index, _ := strings.Cut(arg, ":")
		return clickItem(form, id, index)
	case "type":
		return typeText(arg)
	case "key":
		vk, ok := keyNames[arg]
		if !ok {
			return fmt.Errorf("key %q is not Enter, Escape or Tab", arg)
		}
		return keys([]keyboardInput{{inputType: inputKeyboard, vk: vk}, {inputType: inputKeyboard, vk: vk, flags: keyeventfKeyUp}})
	case "stop":
		return keys([]keyboardInput{
			{inputType: inputKeyboard, vk: vkShift}, {inputType: inputKeyboard, vk: vkEscape},
			{inputType: inputKeyboard, vk: vkEscape, flags: keyeventfKeyUp}, {inputType: inputKeyboard, vk: vkShift, flags: keyeventfKeyUp},
		})
	}
	return errUnknownAction
}

func control(form windows.HWND, id string) (windows.HWND, error) {
	n, err := strconv.Atoi(id)
	if err != nil {
		return 0, fmt.Errorf("control id %q: %w", id, err)
	}
	h, _, _ := getDlgItem.Call(uintptr(form), uintptr(n))
	if h == 0 {
		return 0, fmt.Errorf("the form has no control %d", n)
	}
	return windows.HWND(h), nil
}

// clickItem clicks an item of a drop-down whose list is open.
func clickItem(form windows.HWND, id, index string) error {
	combo, err := control(form, id)
	if err != nil {
		return err
	}
	i, err := strconv.Atoi(index)
	if err != nil {
		return fmt.Errorf("item index %q: %w", index, err)
	}
	info := comboBoxInfo{}
	info.size = uint32(unsafe.Sizeof(info))
	if ok, _, err := getComboBoxInfo.Call(uintptr(combo), uintptr(unsafe.Pointer(&info))); ok == 0 {
		return fmt.Errorf("read the drop-down: %w", err)
	}
	var r rect
	if res, _, _ := sendMessageW.Call(uintptr(info.list), lbGetItemRect, uintptr(i), uintptr(unsafe.Pointer(&r))); int32(res) < 0 {
		return fmt.Errorf("the list has no item %d", i)
	}
	pt := struct{ x, y int32 }{(r.left + r.right) / 2, (r.top + r.bottom) / 2}
	clientToScreen.Call(uintptr(info.list), uintptr(unsafe.Pointer(&pt)))
	return click(pt.x, pt.y)
}

func click(x, y int32) error {
	if ok, _, err := setCursorPos.Call(uintptr(x), uintptr(y)); ok == 0 {
		return fmt.Errorf("move the mouse: %w", err)
	}
	in := []mouseInput{{inputType: inputMouse, flags: mouseLeftDown}, {inputType: inputMouse, flags: mouseLeftUp}}
	if n, _, err := sendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0])); int(n) != len(in) {
		return fmt.Errorf("click: %w", err)
	}
	return nil
}

func typeText(text string) error {
	var in []keyboardInput
	for _, c := range windows.StringToUTF16(text) {
		if c == 0 {
			break
		}
		in = append(in,
			keyboardInput{inputType: inputKeyboard, scan: c, flags: keyeventfUnicod},
			keyboardInput{inputType: inputKeyboard, scan: c, flags: keyeventfUnicod | keyeventfKeyUp})
	}
	return keys(in)
}

func keys(in []keyboardInput) error {
	if n, _, err := sendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0])); int(n) != len(in) {
		return fmt.Errorf("type: %w", err)
	}
	return nil
}

// INPUT is 40 bytes on 64-bit Windows.
var (
	_ [40 - unsafe.Sizeof(keyboardInput{})]byte
	_ [unsafe.Sizeof(keyboardInput{}) - 40]byte
	_ [40 - unsafe.Sizeof(mouseInput{})]byte
	_ [unsafe.Sizeof(mouseInput{}) - 40]byte
)
