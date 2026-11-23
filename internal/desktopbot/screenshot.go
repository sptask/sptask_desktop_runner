package desktopbot

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
)

const (
	// screenshotMaxBytes caps the failure screenshot, as in the browser bot.
	screenshotMaxBytes = 256 * 1024
	// screenshotMinSide stops shrinking before the picture is unreadable.
	screenshotMinSide = 200
)

// screenshotQualities are the JPEG qualities tried before the picture is
// made smaller.
var screenshotQualities = []int{60, 35}

var errScreenshotTooLarge = errors.New("the window picture does not fit the screenshot limit")

// encodeScreenshot returns img as a base64 JPEG of at most
// screenshotMaxBytes, halving its size until it fits.
func encodeScreenshot(img image.Image) (string, error) {
	for {
		for _, q := range screenshotQualities {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
				return "", fmt.Errorf("encode the window picture: %w", err)
			}
			if buf.Len() <= screenshotMaxBytes {
				return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
			}
		}
		b := img.Bounds()
		if b.Dx()/2 < screenshotMinSide || b.Dy()/2 < screenshotMinSide {
			return "", errScreenshotTooLarge
		}
		img = halve(img)
	}
}

// halve shrinks img to half its width and height, averaging each 2x2 block.
func halve(img image.Image) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()/2, b.Dy()/2))
	for y := 0; y < out.Rect.Dy(); y++ {
		for x := 0; x < out.Rect.Dx(); x++ {
			var sum [4]uint32
			for _, d := range [4]image.Point{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
				r, g, bl, a := img.At(b.Min.X+2*x+d.X, b.Min.Y+2*y+d.Y).RGBA()
				sum[0] += r
				sum[1] += g
				sum[2] += bl
				sum[3] += a
			}
			i := out.PixOffset(x, y)
			for c := range sum {
				out.Pix[i+c] = uint8(sum[c] / 4 >> 8)
			}
		}
	}
	return out
}

// screenshotFitsFailure says whether a failure with this code happened at a
// window the bot may photograph. A program that is closed, refused or behind
// the lock screen is never photographed.
func screenshotFitsFailure(code string) bool {
	switch code {
	case CodeAppNotRunning, CodeAppBlocked, CodeAppNotApproved, CodeWorkstationLocked,
		CodeInvalidRecording, CodeUnsupportedPlatform, CodeBusy:
		return false
	}
	return true
}

// visiblePart is the part of a painted window picture inside the window's
// visible frame. screen and frame are in screen pixels; the picture can be
// smaller when Windows stretches the program on the screen.
func visiblePart(screen, frame, picture image.Rectangle) image.Rectangle {
	if screen.Dx() <= 0 || screen.Dy() <= 0 {
		return picture
	}
	sx := float64(picture.Dx()) / float64(screen.Dx())
	sy := float64(picture.Dy()) / float64(screen.Dy())
	scale := func(v int, s float64) int { return int(math.Round(float64(v) * s)) }
	part := image.Rect(
		picture.Min.X+scale(frame.Min.X-screen.Min.X, sx),
		picture.Min.Y+scale(frame.Min.Y-screen.Min.Y, sy),
		picture.Max.X-scale(screen.Max.X-frame.Max.X, sx),
		picture.Max.Y-scale(screen.Max.Y-frame.Max.Y, sy),
	).Intersect(picture)
	if part.Empty() {
		return picture
	}
	return part
}
