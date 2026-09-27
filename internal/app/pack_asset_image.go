package app

import (
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
)

// serveTGAPreview converts the common uncompressed and RLE true-color TGA
// variants used by Bedrock packs into a browser-readable PNG.
func serveTGAPreview(w http.ResponseWriter, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var header [18]byte
	if _, err = io.ReadFull(f, header[:]); err != nil {
		return err
	}
	if header[1] != 0 || (header[2] != 2 && header[2] != 10) || (header[16] != 24 && header[16] != 32) {
		return errors.New("Unsupported TGA format")
	}
	width, height := int(binary.LittleEndian.Uint16(header[12:14])), int(binary.LittleEndian.Uint16(header[14:16]))
	if width == 0 || height == 0 || width*height > 16_000_000 {
		return errors.New("TGA dimensions are too large")
	}
	if _, err = f.Seek(int64(header[0]), io.SeekCurrent); err != nil {
		return err
	}
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	bpp := int(header[16] / 8)
	var pixel [4]byte
	writePixel := func(i int) {
		x, y := i%width, i/width
		if header[17]&0x10 != 0 {
			x = width - 1 - x
		}
		if header[17]&0x20 == 0 {
			y = height - 1 - y
		}
		alpha := uint8(255)
		if bpp == 4 {
			alpha = pixel[3]
		}
		img.SetNRGBA(x, y, color.NRGBA{R: pixel[2], G: pixel[1], B: pixel[0], A: alpha})
	}
	readPixel := func() error { _, err := io.ReadFull(f, pixel[:bpp]); return err }
	for i := 0; i < width*height; {
		if header[2] == 2 {
			if err := readPixel(); err != nil {
				return err
			}
			writePixel(i)
			i++
			continue
		}
		var packet [1]byte
		if _, err := io.ReadFull(f, packet[:]); err != nil {
			return err
		}
		count := int(packet[0]&0x7f) + 1
		if i+count > width*height {
			return errors.New("Invalid TGA run length")
		}
		if packet[0]&0x80 != 0 {
			if err := readPixel(); err != nil {
				return err
			}
			for j := 0; j < count; j++ {
				writePixel(i)
				i++
			}
		} else {
			for j := 0; j < count; j++ {
				if err := readPixel(); err != nil {
					return err
				}
				writePixel(i)
				i++
			}
		}
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	return png.Encode(w, img)
}
