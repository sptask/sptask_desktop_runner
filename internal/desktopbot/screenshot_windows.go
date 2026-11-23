//go:build windows && (amd64 || arm64)

package desktopbot

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCreateCompatibleDC = modGdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection   = modGdi32.NewProc("CreateDIBSection")
	procDeleteDC           = modGdi32.NewProc("DeleteDC")
	procPrintWindow        = modUser32.NewProc("PrintWindow")
	procGetWindowRect      = modUser32.NewProc("GetWindowRect")
	procIsIconic           = modUser32.NewProc("IsIconic")
	procSetThreadDpiCtx    = modUser32.NewProc("SetThreadDpiAwarenessContext")
	procGetWindowDpiCtx    = modUser32.NewProc("GetWindowDpiAwarenessContext")
	modDwmapi              = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmGetWindowAttr   = modDwmapi.NewProc("DwmGetWindowAttribute")
)

const (
	// pwRenderFullContent asks the program to paint its whole window, also
	// the parts drawn by DirectComposition (WPF, modern controls).
	pwRenderFullContent = 2
	dibRGBColors        = 0
	biRGB               = 0
	shotBitsPerPixel    = 32
	// dwmaExtendedFrameBounds is the visible frame, without the invisible
	// resize borders that GetWindowRect includes.
	dwmaExtendedFrameBounds = 9
)

type bitmapInfoHeader struct {
	size          uint32
	width         int32
	height        int32
	planes        uint16
	bitCount      uint16
	compression   uint32
	sizeImage     uint32
	xPelsPerMeter int32
	yPelsPerMeter int32
	clrUsed       uint32
	clrImportant  uint32
}

// failureShot photographs the program window of the failed step, or the
// same program's window in front (an error message box usually explains the
// failure). Only that window is drawn: other programs on the screen, even
// when they cover it, are never in the picture.
func (p *player) failureShot(step *Step) (string, error) {
	win, ok, err := findTopWindow(step.Window)
	if err != nil {
		return "", fmt.Errorf("find the program window: %w", err)
	}
	if !ok {
		return "", errors.New("the program window is closed")
	}
	if err := p.policy.check(win.exe, p.allowed); err != nil {
		return "", err
	}
	if processElevated(win.pid) {
		return "", errors.New("the program runs as administrator")
	}
	target := win.hwnd
	if fg := rootWindow(windows.GetForegroundWindow()); fg != 0 && fg != win.hwnd && windowPID(fg) == win.pid {
		target = fg
	}
	img, err := windowPicture(target)
	if err != nil {
		return "", err
	}
	return encodeScreenshot(img)
}

// windowPicture asks the window to paint itself into a bitmap, without the
// invisible resize borders around it.
//
// A program that does not know about display scaling paints at its own,
// smaller size while Windows stretches it on the screen. The thread asks in
// the window's own scaling mode, so the bitmap is the size the program
// paints; the borders are measured on the screen and scaled to match.
func windowPicture(h windows.HWND) (image.Image, error) {
	if iconic, _, _ := procIsIconic.Call(uintptr(h)); iconic != 0 {
		return nil, errors.New("the window is minimised")
	}
	if procSetThreadDpiCtx.Find() != nil || procGetWindowDpiCtx.Find() != nil {
		return nil, errors.New("this Windows version cannot paint a window by its scaling mode")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	prev, _, _ := procSetThreadDpiCtx.Call(dpiPerMonitorAware2)
	if prev == 0 {
		return nil, errors.New("switch to screen pixels")
	}
	defer procSetThreadDpiCtx.Call(prev)
	screen, err := windowRect(h)
	if err != nil {
		return nil, err
	}
	frame := screen
	if hr, _, _ := procDwmGetWindowAttr.Call(uintptr(h), dwmaExtendedFrameBounds, uintptr(unsafe.Pointer(&frame)), unsafe.Sizeof(frame)); hr != 0 {
		frame = screen // no composition: the window has no invisible borders
	}

	own, _, _ := procGetWindowDpiCtx.Call(uintptr(h))
	if own == 0 {
		return nil, errors.New("read the window's scaling mode")
	}
	if ok, _, _ := procSetThreadDpiCtx.Call(own); ok == 0 {
		return nil, errors.New("switch to the window's scaling mode")
	}
	painted, err := windowRect(h)
	if err != nil {
		return nil, err
	}
	img, err := paintWindow(h, int(painted.Right-painted.Left), int(painted.Bottom-painted.Top))
	if err != nil {
		return nil, err
	}
	return img.SubImage(visiblePart(imageRect(screen), imageRect(frame), img.Rect)), nil
}

func windowRect(h windows.HWND) (windows.Rect, error) {
	var r windows.Rect
	if ok, _, err := procGetWindowRect.Call(uintptr(h), uintptr(unsafe.Pointer(&r))); ok == 0 {
		return r, fmt.Errorf("read the window size: %w", err)
	}
	return r, nil
}

// paintWindow has the window paint itself into a width x height bitmap.
func paintWindow(h windows.HWND, width, height int) (*image.RGBA, error) {
	if width <= 0 || height <= 0 {
		return nil, errors.New("the window has no size")
	}
	dc, _, err := procCreateCompatibleDC.Call(0)
	if dc == 0 {
		return nil, fmt.Errorf("create a drawing surface: %w", err)
	}
	defer procDeleteDC.Call(dc)
	info := bitmapInfoHeader{width: int32(width), height: -int32(height), planes: 1, bitCount: shotBitsPerPixel, compression: biRGB}
	info.size = uint32(unsafe.Sizeof(info))
	var bits unsafe.Pointer
	bmp, _, err := procCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&info)), dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		return nil, fmt.Errorf("create a %dx%d bitmap: %w", width, height, err)
	}
	defer procDeleteObject.Call(bmp)
	old, _, _ := procSelectObject.Call(dc, bmp)
	defer procSelectObject.Call(dc, old)
	if ok, _, err := procPrintWindow.Call(uintptr(h), dc, pwRenderFullContent); ok == 0 {
		return nil, fmt.Errorf("the window did not paint itself: %w", err)
	}
	// The bitmap is top-down BGRA; the picture is opaque.
	src := unsafe.Slice((*byte)(bits), width*height*4)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < len(src); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = src[i+2], src[i+1], src[i], 0xff
	}
	return img, nil
}

func imageRect(r windows.Rect) image.Rectangle {
	return image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom))
}
