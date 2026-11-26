// SPDX-License-Identifier: Apache-2.0

// Package shots turns an uploaded screenshot into an image the broker may store and serve
// (harness screenshots phase B, "never an arbitrary file"): only a PNG or a JPEG, bounded by its
// header before it is decoded, then re-encoded from its pixels alone, so no metadata, ancillary
// chunk, polyglot or trailing byte of the upload survives. It is pure: no I/O, no clock.
package shots

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
)

// JPEGQuality is the fallback encoding's quality, for an image whose PNG is over the limit.
const JPEGQuality = 80

var (
	// ErrNotAnImage is an upload that is not a whole PNG or JPEG.
	ErrNotAnImage = errors.New("not a PNG or JPEG image")
	// ErrDimensions is an image of no pixels, or wider or taller than allowed. It is decided from
	// the header, before any pixel is decoded, so a decompression bomb costs nothing.
	ErrDimensions = errors.New("image dimensions out of bounds")
	// ErrTooLarge is an image still over the limit once re-encoded as JPEG.
	ErrTooLarge = errors.New("image too large once re-encoded")
)

var (
	pngMagic  = []byte("\x89PNG\r\n\x1a\n")
	jpegMagic = []byte{0xFF, 0xD8, 0xFF}
)

// Normalize decodes b, a PNG or a JPEG of at most maxW×maxH pixels, and re-encodes it: a PNG as
// PNG, or as JPEG at JPEGQuality when that PNG is over limit bytes; a JPEG as JPEG at JPEGQuality.
// It returns the new bytes, their media type and the image's size. The format is read from b's
// magic bytes and decoded by that format's own package, never through image.Decode's registry, so
// a format another import registers (GIF, WebP) is still refused.
func Normalize(b []byte, maxW, maxH, limit int) (out []byte, mime string, w, h int, err error) {
	var (
		decodeConfig func([]byte) (image.Config, error)
		decode       func([]byte) (image.Image, error)
	)
	switch {
	case bytes.HasPrefix(b, pngMagic):
		decodeConfig = func(b []byte) (image.Config, error) { return png.DecodeConfig(bytes.NewReader(b)) }
		decode = func(b []byte) (image.Image, error) { return png.Decode(bytes.NewReader(b)) }
	case bytes.HasPrefix(b, jpegMagic):
		decodeConfig = func(b []byte) (image.Config, error) { return jpeg.DecodeConfig(bytes.NewReader(b)) }
		decode = func(b []byte) (image.Image, error) { return jpeg.Decode(bytes.NewReader(b)) }
	default:
		return nil, "", 0, 0, ErrNotAnImage
	}
	cfg, err := decodeConfig(b)
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("%w: %w", ErrNotAnImage, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxW || cfg.Height > maxH {
		return nil, "", 0, 0, fmt.Errorf("%w: %dx%d, at most %dx%d", ErrDimensions, cfg.Width, cfg.Height, maxW, maxH)
	}
	img, err := decode(b)
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("%w: %w", ErrNotAnImage, err)
	}
	w, h = img.Bounds().Dx(), img.Bounds().Dy()
	var buf bytes.Buffer
	if bytes.HasPrefix(b, pngMagic) {
		if err := png.Encode(&buf, img); err != nil {
			return nil, "", 0, 0, fmt.Errorf("shots: encode png: %w", err)
		}
		if buf.Len() <= limit {
			return buf.Bytes(), "image/png", w, h, nil
		}
		buf.Reset()
	}
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return nil, "", 0, 0, fmt.Errorf("shots: encode jpeg: %w", err)
	}
	if buf.Len() > limit {
		return nil, "", 0, 0, fmt.Errorf("%w: %d bytes as JPEG, at most %d", ErrTooLarge, buf.Len(), limit)
	}
	return buf.Bytes(), "image/jpeg", w, h, nil
}
