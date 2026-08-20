package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"

	// Decoders. Each of these registers itself with image.RegisterFormat, so
	// image.Decode sniffs the format from the magic bytes and dispatches.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/tiff"

	"github.com/gen2brain/heic" // HEIC decode (CGo-free, handles grid/tiled iPhone images)
	_ "github.com/gen2brain/webp"

	// NOTE: do not also import golang.org/x/image/webp -- gen2brain/webp
	// already registers the "webp" format and provides the encoder too.
	"golang.org/x/image/draw"
)

func init() {
	// gen2brain/heic registers the heic, heix, hevc, hevx and msf1 brands, but
	// not mif1 -- the generic ISOBMFF image brand that libheif and several
	// camera pipelines emit as the major brand. Without this, image.Decode
	// fails to sniff those files even though the decoder handles them fine.
	image.RegisterFormat("heic", "????ftypmif1", heic.Decode, heic.DecodeConfig)
}

// maxPixels bounds the input we are willing to decode. libvips used to protect
// us here implicitly via shrink-on-load; pure Go must decode at full
// resolution, so a huge upload would otherwise blow up Lambda's memory.
const maxPixels = 40_000_000

// decodeImage decodes an uploaded image and applies its EXIF orientation.
// It returns the oriented image and the name of the format that was detected.
func decodeImage(data []byte) (image.Image, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("unrecognised image format: %w", err)
	}
	if px := cfg.Width * cfg.Height; px > maxPixels {
		return nil, format, fmt.Errorf("image too large: %dx%d = %d pixels, limit is %d",
			cfg.Width, cfg.Height, px, maxPixels)
	}

	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, format, fmt.Errorf("decoding %s: %w", format, err)
	}

	return applyOrientation(img, readOrientation(data, format)), format, nil
}

// readOrientation extracts the EXIF orientation tag (1-8). It never fails:
// anything unparseable yields 1, which is the identity transform. A corrupt
// metadata block must not fail the whole job.
func readOrientation(data []byte, format string) int {
	switch format {
	case "jpeg":
		return jpegOrientation(data)
	case "heic":
		if ex, err := heic.DecodeExif(bytes.NewReader(data)); err == nil && ex != nil {
			if ex.Orientation >= 1 && ex.Orientation <= 8 {
				return ex.Orientation
			}
		}
	}
	return 1
}

// jpegOrientation walks JPEG markers looking for the APP1 segment holding an
// Exif block, then reads IFD0 tag 0x0112. Every read is bounds-checked.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return 1 // not at a marker boundary; give up
		}
		marker := data[i+1]
		// Standalone markers carry no length.
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 {
			return 1 // start of scan / end of image: no EXIF before pixel data
		}
		if i+4 > len(data) {
			return 1
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(data) {
			return 1
		}
		if marker == 0xE1 {
			payload := data[i+4 : i+2+segLen]
			if len(payload) > 6 && bytes.Equal(payload[:6], []byte("Exif\x00\x00")) {
				if o := tiffOrientation(payload[6:]); o != 0 {
					return o
				}
			}
		}
		i += 2 + segLen
	}
	return 1
}

// tiffOrientation parses a TIFF header plus IFD0 and returns tag 0x0112,
// or 0 if it is absent or malformed.
func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch {
	case tiff[0] == 'I' && tiff[1] == 'I':
		bo = binary.LittleEndian
	case tiff[0] == 'M' && tiff[1] == 'M':
		bo = binary.BigEndian
	default:
		return 0
	}
	if bo.Uint16(tiff[2:4]) != 42 {
		return 0
	}
	offset := int(bo.Uint32(tiff[4:8]))
	if offset < 8 || offset+2 > len(tiff) {
		return 0
	}
	count := int(bo.Uint16(tiff[offset : offset+2]))
	entries := offset + 2
	for e := 0; e < count; e++ {
		p := entries + e*12
		if p+12 > len(tiff) {
			return 0
		}
		tag := bo.Uint16(tiff[p : p+2])
		if tag != 0x0112 {
			continue
		}
		if typ := bo.Uint16(tiff[p+2 : p+4]); typ != 3 { // 3 = SHORT
			return 0
		}
		// A SHORT fits inline in the 4-byte value field.
		v := int(bo.Uint16(tiff[p+8 : p+10]))
		if v >= 1 && v <= 8 {
			return v
		}
		return 0
	}
	return 0
}

// applyOrientation rewrites pixels so the image displays upright, matching what
// libvips did via NoAutoRotate:false. Orientations 5-8 swap width and height,
// which changes every downstream smartDims result.
func applyOrientation(src image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()

	// 5-8 are the transposing cases.
	dstW, dstH := w, h
	if orientation >= 5 {
		dstW, dstH = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))

	// Work from an RGBA copy so the inner loop is a straight memory read.
	srcRGBA, ok := src.(*image.RGBA)
	if !ok || srcRGBA.Bounds() != b {
		tmp := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Copy(tmp, image.Point{}, src, b, draw.Src, nil)
		srcRGBA = tmp
	}

	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			var sx, sy int
			switch orientation {
			case 2: // mirror horizontal
				sx, sy = w-1-x, y
			case 3: // rotate 180
				sx, sy = w-1-x, h-1-y
			case 4: // mirror vertical
				sx, sy = x, h-1-y
			case 5: // transpose
				sx, sy = y, x
			case 6: // rotate 90 CW
				sx, sy = y, h-1-x
			case 7: // transverse
				sx, sy = w-1-y, h-1-x
			case 8: // rotate 90 CCW
				sx, sy = w-1-y, x
			}
			si := srcRGBA.PixOffset(sx, sy)
			di := dst.PixOffset(x, y)
			copy(dst.Pix[di:di+4], srcRGBA.Pix[si:si+4])
		}
	}
	return dst
}
