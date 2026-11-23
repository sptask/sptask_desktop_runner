package desktopbot

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"testing"
)

func decodeShot(t *testing.T, shot string) image.Image {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(shot)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > screenshotMaxBytes {
		t.Fatalf("screenshot is %d bytes, over the %d limit", len(raw), screenshotMaxBytes)
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestEncodeScreenshotKeepsASmallWindowWhole(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 460, 380))
	for i := range img.Pix {
		img.Pix[i] = 0xee
	}
	shot, err := encodeScreenshot(img)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeShot(t, shot).Bounds().Size(); got != image.Pt(460, 380) {
		t.Fatalf("a small window was resized to %v", got)
	}
}

func TestEncodeScreenshotShrinksUntilItFits(t *testing.T) {
	// Noise does not compress: a large noisy window must be made smaller.
	img := image.NewRGBA(image.Rect(0, 0, 1600, 1200))
	rnd := rand.New(rand.NewSource(1))
	rnd.Read(img.Pix)
	shot, err := encodeScreenshot(img)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeShot(t, shot).Bounds().Size()
	if got.X >= 1600 || got.X*1200 != got.Y*1600 {
		t.Fatalf("expected a smaller picture with the same shape, got %v", got)
	}
}

func TestEncodeScreenshotGivesUpBelowTheMinimum(t *testing.T) {
	old := screenshotQualities
	t.Cleanup(func() { screenshotQualities = old })
	screenshotQualities = []int{100}
	// Noise at quality 100 is larger than the limit, and halving this
	// picture would go under the minimum side.
	side := 2*screenshotMinSide - 1
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	rand.New(rand.NewSource(2)).Read(img.Pix)
	if _, err := encodeScreenshot(img); !errors.Is(err, errScreenshotTooLarge) {
		t.Fatalf("expected errScreenshotTooLarge, got %v", err)
	}
}

func TestHalveAveragesBlocks(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	img.Set(1, 0, color.RGBA{R: 100, A: 255})
	img.Set(0, 1, color.RGBA{R: 0, A: 255})
	img.Set(1, 1, color.RGBA{R: 100, A: 255})
	out := halve(img)
	if out.Bounds().Size() != image.Pt(2, 1) {
		t.Fatalf("size %v", out.Bounds().Size())
	}
	if got := out.RGBAAt(0, 0); got.R != 100 || got.A != 255 {
		t.Fatalf("average %v", got)
	}
}

func TestScreenshotFitsFailure(t *testing.T) {
	for _, code := range []string{CodeAppNotRunning, CodeAppBlocked, CodeAppNotApproved, CodeWorkstationLocked, CodeInvalidRecording, CodeBusy, CodeUnsupportedPlatform} {
		if screenshotFitsFailure(code) {
			t.Errorf("%s must not be photographed", code)
		}
	}
	for _, code := range []string{CodeSelectorNotFound, CodeValueNotSet, CodeWrongWindow, CodeAssertionFailed, CodeTimeout, CodeCancelled, CodeDesktopError} {
		if !screenshotFitsFailure(code) {
			t.Errorf("%s should come with a screenshot", code)
		}
	}
}

func TestVisiblePartDropsTheInvisibleBorders(t *testing.T) {
	screen := image.Rect(100, 100, 700, 550) // what GetWindowRect gives
	frame := image.Rect(110, 100, 690, 540)  // the visible frame
	// The program paints at its own size; Windows stretches it by 1.25.
	picture := image.Rect(0, 0, 480, 360)
	if got, want := visiblePart(screen, frame, picture), image.Rect(8, 0, 472, 352); got != want {
		t.Fatalf("scaled: got %v, want %v", got, want)
	}
	if got, want := visiblePart(screen, frame, image.Rect(0, 0, 600, 450)), image.Rect(10, 0, 590, 440); got != want {
		t.Fatalf("unscaled: got %v, want %v", got, want)
	}
	if got := visiblePart(screen, image.Rect(0, 0, 0, 0), picture); got != picture {
		t.Fatalf("a frame outside the window keeps the whole picture, got %v", got)
	}
}
