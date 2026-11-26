// SPDX-License-Identifier: Apache-2.0

package browsergate

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // registers the PNG decoder image.Decode reads screenshots with
)

// ImageLimit is the size cap of one screenshot, in bytes.
const ImageLimit = 512 << 10

// maxPixels bounds what CapImage decodes: a full-page capture of a long page, about 30
// megapixels, still fits; a decompression bomb does not.
const maxPixels = 32 << 20

// maxHalvings is how many times CapImage halves an image before giving up.
const maxHalvings = 3

// The refusals CapImage reports.
var (
	ErrTooLarge         = errors.New("image_too_large")
	ErrUnsupportedImage = errors.New("image_unsupported")
)

// formats maps the MIME types a screenshot may carry to the decoder name image.DecodeConfig reports.
var formats = map[string]string{"image/png": "png", "image/jpeg": "jpeg"}

// CapImage returns b unchanged when it is a PNG or JPEG of at most limit bytes. A larger one is
// re-encoded as JPEG quality 70, then halved up to three times until it fits, else ErrTooLarge.
// Bytes that are not the PNG or JPEG mime names are ErrUnsupportedImage.
func CapImage(b []byte, mime string, limit int) ([]byte, string, error) {
	want, ok := formats[mime]
	if !ok {
		return nil, "", fmt.Errorf("%w: %q", ErrUnsupportedImage, mime)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || format != want {
		return nil, "", fmt.Errorf("%w: the bytes are not a %s", ErrUnsupportedImage, mime)
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width*cfg.Height > maxPixels {
		return nil, "", fmt.Errorf("%w: %dx%d", ErrTooLarge, cfg.Width, cfg.Height)
	}
	if len(b) <= limit {
		return b, mime, nil
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrUnsupportedImage, err)
	}
	for pass := 0; pass <= maxHalvings; pass++ {
		if pass > 0 {
			img = halve(img)
		}
		var out bytes.Buffer
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 70}); err != nil {
			return nil, "", fmt.Errorf("encode jpeg: %w", err)
		}
		if out.Len() <= limit {
			return out.Bytes(), "image/jpeg", nil
		}
	}
	return nil, "", fmt.Errorf("%w: over %d bytes after %d halvings", ErrTooLarge, limit, maxHalvings)
}

// halve scales img to half its width and height by nearest neighbour: a screenshot only needs to
// stay legible, and the standard library has no resampler.
func halve(img image.Image) image.Image {
	b := img.Bounds()
	w, h := max(b.Dx()/2, 1), max(b.Dy()/2, 1)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			dst.Set(x, y, img.At(b.Min.X+2*x, b.Min.Y+2*y))
		}
	}
	return dst
}
