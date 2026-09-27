package app

import (
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestServeTGAPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "texture.tga")
	// Two pixels in a top-origin, 24-bit true-color TGA: red then green.
	data := make([]byte, 18)
	data[2], data[12], data[14], data[16], data[17] = 2, 2, 1, 24, 0x20
	data = append(data, 0, 0, 255, 0, 255, 0)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err := serveTGAPreview(w, path); err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(0, 0).RGBA()
	if r != 65535 || g != 0 || b != 0 {
		t.Fatalf("first pixel = %d,%d,%d", r, g, b)
	}
	r, g, b, _ = img.At(1, 0).RGBA()
	if r != 0 || g != 65535 || b != 0 {
		t.Fatalf("second pixel = %d,%d,%d", r, g, b)
	}
}
