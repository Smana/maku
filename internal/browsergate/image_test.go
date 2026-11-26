// SPDX-License-Identifier: Apache-2.0

package browsergate

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"testing"
)

// noisePNG is a w×h PNG of random pixels, which compresses badly.
func noisePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(r.UintN(256))
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 0xff
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestCapImage(t *testing.T) {
	big := noisePNG(t, 1920, 1080)
	if len(big) <= ImageLimit {
		t.Fatalf("fixture %d bytes is not over the limit", len(big))
	}
	out, mime, err := CapImage(big, "image/png", ImageLimit)
	if err != nil || len(out) > ImageLimit || mime != "image/jpeg" {
		t.Fatal(len(out), mime, err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Fatal("the capped image decodes as JPEG:", err)
	}
	small := noisePNG(t, 8, 8)
	if out, mime, _ := CapImage(small, "image/png", ImageLimit); !bytes.Equal(out, small) || mime != "image/png" {
		t.Error("under the limit: unchanged")
	}
}

func TestCapImageRefuses(t *testing.T) {
	var j bytes.Buffer
	if err := jpeg.Encode(&j, image.NewGray(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		b     []byte
		mime  string
		limit int
		want  error
	}{
		{"bytes that are not an image", []byte("<svg/>"), "image/png", ImageLimit, ErrUnsupportedImage},
		{"a mime type other than png or jpeg", noisePNG(t, 4, 4), "image/webp", ImageLimit, ErrUnsupportedImage},
		{"a mime type that disagrees with the bytes", j.Bytes(), "image/png", ImageLimit, ErrUnsupportedImage},
		{"an image three halvings cannot bring under the limit", noisePNG(t, 256, 256), "image/png", 64, ErrTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := CapImage(c.b, c.mime, c.limit); !errors.Is(err, c.want) {
				t.Errorf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestHalve(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 5, 3))
	src.Set(2, 2, color.RGBA{R: 9, A: 255})
	got := halve(src)
	if b := got.Bounds(); b.Dx() != 2 || b.Dy() != 1 {
		t.Fatal(b)
	}
}
