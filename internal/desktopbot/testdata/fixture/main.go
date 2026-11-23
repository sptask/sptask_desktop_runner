//go:build windows

// Command fixture is a small Windows form for the desktop bot's tests: a name
// box, a note box, a password box, a payment drop-down, a city drop-down that also takes typed text, a Save button and a
// disabled Delete button. Every Save writes the form to the -out file as one
// JSON line, so a test sees exactly what the program received and how often.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	idName    = 101
	idNote    = 102
	idSecret  = 103
	idPayment = 104
	idCity    = 105
	idSave    = 201
	idDelete  = 202
	idStatus  = 301
	// idOK is what IsDialogMessage sends for Enter in a field: the form saves.
	idOK = 1

	wsOverlapped   = 0x00CF0000
	wsVisible      = 0x10000000
	wsChild        = 0x40000000
	wsBorder       = 0x00800000
	wsTabStop      = 0x00010000
	wsVScroll      = 0x00200000
	wsDisabled     = 0x08000000
	esAutoHScroll  = 0x0080
	esMultiline    = 0x0004
	esPassword     = 0x0020
	esWantReturn   = 0x1000
	cbsDropDown    = 0x0002
	cbsDropDownLst = 0x0003
	bsPushButton   = 0x0000
	wmCommand      = 0x0111
	wmDestroy      = 0x0002
	wmSetText      = 0x000C
	cbAddString    = 0x0143
	cbSetCurSel    = 0x014E
	bnClicked      = 0
	cwUseDefault   = 0x80000000
	colorWindow    = 5
	// wsExTopmost keeps the form above other windows, such as a test browser
	// opened after it: tests click the form where it is on the screen.
	wsExTopmost = 0x00000008
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	registerClassExW = user32.NewProc("RegisterClassExW")
	createWindowExW  = user32.NewProc("CreateWindowExW")
	defWindowProcW   = user32.NewProc("DefWindowProcW")
	getMessageW      = user32.NewProc("GetMessageW")
	translateMessage = user32.NewProc("TranslateMessage")
	dispatchMessageW = user32.NewProc("DispatchMessageW")
	postQuitMessage  = user32.NewProc("PostQuitMessage")
	getWindowTextW   = user32.NewProc("GetWindowTextW")
	sendMessageW     = user32.NewProc("SendMessageW")
	getDlgItem       = user32.NewProc("GetDlgItem")
	isDialogMessageW = user32.NewProc("IsDialogMessageW")

	out   string
	clear bool
	form  windows.HWND
	saves int
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

type msg struct {
	hwnd    windows.HWND
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
	private uint32
}

func text(id int) string {
	h, _, _ := getDlgItem.Call(uintptr(form), uintptr(id))
	buf := make([]uint16, 4096)
	getWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf)
}

func save() error {
	saves++
	secret := text(idSecret)
	record := map[string]any{
		"name": text(idName), "note": text(idNote), "secret_length": len([]rune(secret)),
		"payment": text(idPayment), "city": text(idCity), "save": saves,
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode form: %w", err)
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", out, err)
	}
	defer f.Close()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	if clear {
		// Like many forms, start the next entry empty. A recorder must not take
		// this for the person emptying the fields.
		for _, id := range []int{idName, idNote, idSecret, idCity} {
			h, _, _ := getDlgItem.Call(uintptr(form), uintptr(id))
			sendMessageW.Call(h, wmSetText, 0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(""))))
		}
		combo, _, _ := getDlgItem.Call(uintptr(form), idPayment)
		sendMessageW.Call(combo, cbSetCurSel, ^uintptr(0), 0)
	}
	status, _, _ := getDlgItem.Call(uintptr(form), idStatus)
	sendMessageW.Call(status, wmSetText, 0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(fmt.Sprintf("Kaydedildi: %d", saves)))))
	return nil
}

func wndProc(hwnd windows.HWND, m uint32, wParam, lParam uintptr) uintptr {
	switch m {
	case wmCommand:
		if id := wParam & 0xFFFF; (id == idSave || id == idOK) && (wParam>>16)&0xFFFF == bnClicked {
			if err := save(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			return 0
		}
	case wmDestroy:
		postQuitMessage.Call(0)
		return 0
	}
	r, _, _ := defWindowProcW.Call(uintptr(hwnd), uintptr(m), wParam, lParam)
	return r
}

func child(instance windows.Handle, class, title string, style uintptr, id int, x, y, w, h int32) windows.HWND {
	hwnd, _, _ := createWindowExW.Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(class))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(title))), wsChild|wsVisible|style,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h), uintptr(form), uintptr(id), uintptr(instance), 0)
	return windows.HWND(hwnd)
}

func main() {
	title := flag.String("title", "SparTask Masaüstü Fikstürü", "window title")
	flag.StringVar(&out, "out", "", "file that receives one JSON line per Save")
	flag.BoolVar(&clear, "clear-on-save", false, "empty the form after every Save")
	flag.Parse()
	if out == "" {
		fmt.Fprintln(os.Stderr, "-out is required")
		os.Exit(2)
	}
	runtime.LockOSThread()
	var instance windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &instance); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	className := windows.StringToUTF16Ptr("SpartaskFixtureForm")
	wc := wndClassEx{wndProc: windows.NewCallback(wndProc), instance: instance, className: className, background: colorWindow + 1}
	wc.size = uint32(unsafe.Sizeof(wc))
	if atom, _, err := registerClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		fmt.Fprintln(os.Stderr, "register class:", err)
		os.Exit(2)
	}
	hwnd, _, err := createWindowExW.Call(wsExTopmost, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(*title))),
		wsOverlapped|wsVisible, cwUseDefault, cwUseDefault, 460, 380, 0, 0, uintptr(instance), 0)
	if hwnd == 0 {
		fmt.Fprintln(os.Stderr, "create window:", err)
		os.Exit(2)
	}
	form = windows.HWND(hwnd)
	child(instance, "STATIC", "Müşteri adı:", 0, 0, 16, 18, 110, 20)
	child(instance, "EDIT", "", wsBorder|wsTabStop|esAutoHScroll, idName, 130, 16, 290, 24)
	child(instance, "STATIC", "Not:", 0, 0, 16, 54, 110, 20)
	child(instance, "EDIT", "", wsBorder|wsTabStop|wsVScroll|esMultiline|esWantReturn, idNote, 130, 52, 290, 60)
	child(instance, "STATIC", "Şifre:", 0, 0, 16, 128, 110, 20)
	child(instance, "EDIT", "", wsBorder|wsTabStop|esPassword|esAutoHScroll, idSecret, 130, 126, 290, 24)
	child(instance, "STATIC", "Ödeme:", 0, 0, 16, 164, 110, 20)
	combo := child(instance, "COMBOBOX", "", wsTabStop|wsVScroll|cbsDropDownLst, idPayment, 130, 162, 290, 120)
	for _, item := range []string{"Nakit", "Kart", "Havale"} {
		sendMessageW.Call(uintptr(combo), cbAddString, 0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(item))))
	}
	// A drop-down that also takes typed text: the focus is in its own edit box.
	child(instance, "STATIC", "Şehir:", 0, 0, 16, 200, 110, 20)
	city := child(instance, "COMBOBOX", "", wsTabStop|wsVScroll|cbsDropDown, idCity, 130, 198, 290, 120)
	for _, item := range []string{"Ankara", "İstanbul", "İzmir"} {
		sendMessageW.Call(uintptr(city), cbAddString, 0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(item))))
	}
	child(instance, "BUTTON", "Kaydet", wsTabStop|bsPushButton, idSave, 130, 246, 120, 30)
	child(instance, "BUTTON", "Sil", wsTabStop|bsPushButton|wsDisabled, idDelete, 300, 246, 120, 30)
	child(instance, "STATIC", "Hazır", 0, idStatus, 16, 292, 400, 20)

	var m msg
	for {
		r, _, _ := getMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		if ok, _, _ := isDialogMessageW.Call(uintptr(form), uintptr(unsafe.Pointer(&m))); ok != 0 {
			continue
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&m)))
		dispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}
