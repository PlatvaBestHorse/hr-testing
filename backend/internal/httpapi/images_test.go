package httpapi

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func fixturePNG() []byte {
	im := image.NewNRGBA(image.Rect(0, 0, 8, 6))
	im.Set(0, 0, color.NRGBA{R: 200, A: 255})
	var b bytes.Buffer
	png.Encode(&b, im)
	return b.Bytes()
}
func TestImageNormalization(t *testing.T) {
	for _, kind := range []string{"png", "jpeg"} {
		var source bytes.Buffer
		im := image.NewNRGBA(image.Rect(0, 0, 8, 6))
		if kind == "png" {
			png.Encode(&source, im)
		} else {
			jpeg.Encode(&source, im, nil)
		}
		raw := append(source.Bytes(), []byte("<script>trailing-payload</script>")...)
		clean, meta, err := normalizeImage(raw, "image/"+kind)
		if err != nil || meta.Width != 8 || meta.Height != 6 || meta.Size != len(clean) {
			t.Fatalf("normalize %s: %v", kind, err)
		}
		if bytes.Contains(clean, []byte("trailing-payload")) {
			t.Fatal("preserved trailing payload")
		}
		if _, actual, e := image.Decode(bytes.NewReader(clean)); e != nil || actual != kind {
			t.Fatal("output is not a readable raster")
		}
	}
}
func TestImageValidationLimits(t *testing.T) {
	huge := append([]byte{}, fixturePNG()...)
	binary.BigEndian.PutUint32(huge[16:20], 5000)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	pixels := append([]byte{}, huge...)
	binary.BigEndian.PutUint32(pixels[16:20], 4000)
	binary.BigEndian.PutUint32(pixels[20:24], 4000)
	binary.BigEndian.PutUint32(pixels[29:33], crc32.ChecksumIEEE(pixels[12:29]))
	for _, tc := range []struct {
		name, mime string
		raw        []byte
	}{
		{"empty", "image/png", nil}, {"svg", "image/png", []byte(`<svg onload="alert(1)"></svg>`)}, {"mismatch", "image/jpeg", fixturePNG()},
		{"oversized", "image/png", make([]byte, maxImageBytes+1)}, {"dimension", "image/png", huge}, {"pixel limit", "image/png", pixels},
		{"truncated", "image/png", fixturePNG()[:40]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := normalizeImage(tc.raw, tc.mime); err == nil {
				t.Fatal("accepted invalid image")
			}
		})
	}
	var b bytes.Buffer
	w := imageWriter{Buffer: &b}
	if _, err := w.Write(make([]byte, maxImageBytes+1)); err == nil || b.Len() != 0 {
		t.Fatal("encoder output exceeded memory limit")
	}
}
