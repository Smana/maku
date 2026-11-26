// SPDX-License-Identifier: Apache-2.0

package shots

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"testing"
)

func pic(w, h int, noise bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(1, 2))
	for y := range h {
		for x := range w {
			c := color.RGBA{uint8(x), uint8(y), 128, 255}
			if noise {
				// Low-amplitude noise on a gradient: incompressible for PNG, cheap for JPEG.
				c = color.RGBA{uint8(x) + uint8(r.UintN(16)), uint8(y) + uint8(r.UintN(16)), 128 + uint8(r.UintN(16)), 255}
			}
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func chunk(typ string, data []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(typ)
	b.Write(data)
	crc := crc32.NewIEEE()
	crc.Write([]byte(typ))
	crc.Write(data)
	_ = binary.Write(&b, binary.BigEndian, crc.Sum32())
	return b.Bytes()
}

// withText inserts a tEXt chunk after IHDR (8-byte signature + 25-byte IHDR chunk).
func withText(p []byte, text string) []byte {
	out := append([]byte{}, p[:33]...)
	out = append(out, chunk("tEXt", []byte("Comment\x00"+text))...)
	return append(out, p[33:]...)
}

func TestNormalizeStripsWhatIsNotPixels(t *testing.T) {
	src := withText(encodePNG(t, pic(64, 32, false)), "SECRET-CHUNK")
	src = append(src, bytes.Repeat([]byte("TRAILING"), 128)...)
	out, mime, w, h, err := Normalize(src, 1920, 1080, 1<<20)
	if err != nil || mime != "image/png" || w != 64 || h != 32 {
		t.Fatalf("Normalize: %s %dx%d %v", mime, w, h, err)
	}
	if bytes.Contains(out, []byte("SECRET-CHUNK")) || bytes.Contains(out, []byte("TRAILING")) || bytes.Contains(out, []byte("tEXt")) {
		t.Fatal("a text chunk or trailing bytes survived the re-encode")
	}
	if _, err := png.Decode(bytes.NewReader(out)); err != nil {
		t.Fatalf("the output is not a PNG: %v", err)
	}

	var j bytes.Buffer
	if err := jpeg.Encode(&j, pic(64, 32, false), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	// An APP1 (EXIF) segment right after SOI.
	exif := append([]byte("Exif\x00\x00"), []byte("GPS-SECRET")...)
	app1 := append([]byte{0xFF, 0xE1, byte((len(exif) + 2) >> 8), byte(len(exif) + 2)}, exif...)
	jsrc := append(append(append([]byte{}, j.Bytes()[:2]...), app1...), j.Bytes()[2:]...)
	out, mime, _, _, err = Normalize(jsrc, 1920, 1080, 1<<20)
	if err != nil || mime != "image/jpeg" {
		t.Fatalf("Normalize JPEG: %s %v", mime, err)
	}
	if bytes.Contains(out, []byte("GPS-SECRET")) || bytes.Contains(out, []byte("Exif")) {
		t.Fatal("EXIF survived the re-encode")
	}
}

func TestNormalizeRefuses(t *testing.T) {
	var g bytes.Buffer
	if err := gif.Encode(&g, pic(1, 1, false), nil); err != nil {
		t.Fatal(err)
	}
	// A PNG whose header claims 50000x50000: refused from the header alone.
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 50000)
	binary.BigEndian.PutUint32(ihdr[4:], 50000)
	ihdr[8], ihdr[9] = 8, 2 // 8-bit RGB
	bomb := append([]byte("\x89PNG\r\n\x1a\n"), chunk("IHDR", ihdr)...)
	for name, tc := range map[string]struct {
		b    []byte
		want error
	}{
		"a gif":                  {g.Bytes(), ErrNotAnImage},
		"an svg":                 {[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), ErrNotAnImage},
		"html":                   {[]byte(`<!doctype html><html><body>hi</body></html>`), ErrNotAnImage},
		"empty":                  {nil, ErrNotAnImage},
		"a truncated png":        {encodePNG(t, pic(8, 8, false))[:40], ErrNotAnImage},
		"a 50000x50000 png head": {bomb, ErrDimensions},
		"over the viewport":      {encodePNG(t, pic(1921, 2, false)), ErrDimensions},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, _, err := Normalize(tc.b, 1920, 1080, 1<<20); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNormalizeFallsBackToJPEG(t *testing.T) {
	src := encodePNG(t, pic(1920, 1080, true))
	if len(src) <= 1<<20 {
		t.Fatalf("the noise PNG is %d bytes, want over 1 MiB", len(src))
	}
	out, mime, w, h, err := Normalize(src, 1920, 1080, 1<<20)
	if err != nil || mime != "image/jpeg" || w != 1920 || h != 1080 || len(out) > 1<<20 {
		t.Fatalf("Normalize: %s %dx%d %d bytes %v", mime, w, h, len(out), err)
	}
	if _, _, _, _, err := Normalize(src, 1920, 1080, 1<<10); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("still over the limit as JPEG: %v, want ErrTooLarge", err)
	}
}

func TestFitScalesALargeImageDown(t *testing.T) {
	big := encodePNG(t, pic(3000, 1500, false))
	out, mime, w, h, err := Fit(big, 8192, 8192, 1568, 1<<20)
	if err != nil || w != 1568 || h != 784 || mime != "image/png" || len(out) == 0 {
		t.Fatalf("got %dx%d %s %d bytes, %v; want 1568x784 png", w, h, mime, len(out), err)
	}
	small := encodePNG(t, pic(200, 100, false))
	if _, _, w, h, err := Fit(small, 8192, 8192, 1568, 1<<20); err != nil || w != 200 || h != 100 {
		t.Fatalf("a small image keeps its size: %dx%d %v", w, h, err)
	}
	if _, _, _, _, err := Fit(encodePNG(t, pic(9000, 10, false)), 8192, 8192, 1568, 1<<20); !errors.Is(err, ErrDimensions) {
		t.Fatalf("the header bound still holds before decoding: %v", err)
	}
}
