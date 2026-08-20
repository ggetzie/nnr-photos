package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func synthImage(w, h int) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := m.PixOffset(x, y)
			m.Pix[i+0] = uint8((x * 255) / max(w-1, 1))
			m.Pix[i+1] = uint8((y * 255) / max(h-1, 1))
			m.Pix[i+2] = uint8(((x + y) * 255) / max(w+h-2, 1))
			m.Pix[i+3] = 255
		}
	}
	return m
}

// decodeDerivative decodes one derivative and reports its dimensions and the
// format actually detected from the bytes.
func decodeDerivative(t *testing.T, d Derivative) (image.Image, string) {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(d.Data))
	if err != nil {
		t.Fatalf("%s: decoding output: %v", d.Filename(), err)
	}
	return img, format
}

// TestProcessImageManifest pins the exact output file set. This is the contract
// with the Django app (recipes.models.SCREEN_SIZES x PHOTO_EXTENSIONS plus
// orig.jpeg and thumbnail.jpeg).
func TestProcessImageManifest(t *testing.T) {
	derivatives, err := processImage(synthImage(1600, 1200), getDefaultImageTypes(), getDefaultDims(), defaultThumbSize)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"orig.jpeg", "thumbnail.jpeg",
		"1200.jpeg", "1200.webp", "992.jpeg", "992.webp", "768.jpeg", "768.webp",
		"576.jpeg", "576.webp", "408.jpeg", "408.webp", "320.jpeg", "320.webp",
	}
	got := map[string]bool{}
	for _, d := range derivatives {
		if got[d.Filename()] {
			t.Errorf("duplicate output %s", d.Filename())
		}
		got[d.Filename()] = true
		if len(d.Data) == 0 {
			t.Errorf("%s is empty", d.Filename())
		}
	}
	if len(derivatives) != len(want) {
		t.Errorf("produced %d derivatives, want %d", len(derivatives), len(want))
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing output %s", name)
		}
	}
}

// TestProcessImageDimensions checks every derivative really is the size
// smartDims promised, and that the thumbnail is an exact square.
func TestProcessImageDimensions(t *testing.T) {
	const w, h = 1600, 1200
	dims := getDefaultDims()
	derivatives, err := processImage(synthImage(w, h), getDefaultImageTypes(), dims, defaultThumbSize)
	if err != nil {
		t.Fatal(err)
	}
	orig := ImageSize{w, h}
	for _, d := range derivatives {
		img, _ := decodeDerivative(t, d)
		b := img.Bounds()
		switch d.Name {
		case "orig":
			if b.Dx() != w || b.Dy() != h {
				t.Errorf("orig is %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
			}
		case "thumbnail":
			if b.Dx() != defaultThumbSize || b.Dy() != defaultThumbSize {
				t.Errorf("thumbnail is %dx%d, want %dx%d (must be a square crop)",
					b.Dx(), b.Dy(), defaultThumbSize, defaultThumbSize)
			}
		default:
			want := smartDims(orig, dims[d.Name])
			if b.Dx() != want.Width || b.Dy() != want.Height {
				t.Errorf("%s is %dx%d, want %dx%d", d.Filename(), b.Dx(), b.Dy(), want.Width, want.Height)
			}
		}
	}
}

// TestProcessImageFormats verifies the bytes really are the format the filename
// claims, by sniffing rather than trusting the extension.
func TestProcessImageFormats(t *testing.T) {
	derivatives, err := processImage(synthImage(800, 600), getDefaultImageTypes(), getDefaultDims(), defaultThumbSize)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range derivatives {
		_, format := decodeDerivative(t, d)
		if format != d.Format.String() {
			t.Errorf("%s: bytes are %s, want %s", d.Filename(), format, d.Format)
		}
		switch d.Format {
		case FormatJPEG:
			if !bytes.HasPrefix(d.Data, []byte{0xFF, 0xD8, 0xFF}) {
				t.Errorf("%s: bad JPEG magic", d.Filename())
			}
		case FormatWEBP:
			if len(d.Data) < 12 || !bytes.Equal(d.Data[0:4], []byte("RIFF")) || !bytes.Equal(d.Data[8:12], []byte("WEBP")) {
				t.Errorf("%s: bad WebP magic", d.Filename())
			}
		}
	}
}

// TestProcessImageNeverUpscales: a small input must come out at its original
// size at every breakpoint.
func TestProcessImageNeverUpscales(t *testing.T) {
	const w, h = 200, 150
	derivatives, err := processImage(synthImage(w, h), []ImageFormat{FormatJPEG}, getDefaultDims(), 64)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range derivatives {
		if d.Name == "thumbnail" {
			continue
		}
		img, _ := decodeDerivative(t, d)
		if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
			t.Errorf("%s is %dx%d, want %dx%d (upscaled!)", d.Filename(), b.Dx(), b.Dy(), w, h)
		}
	}
}

// TestThumbnailQuality guards the easily-missed detail that bimg.Thumbnail
// hardcoded quality 95 while every other derivative used 75. At equal
// dimensions the higher-quality encode is materially larger.
func TestThumbnailIsHigherQuality(t *testing.T) {
	src := synthImage(512, 512)
	thumb := coverCrop(src, 128)
	at95, err := encode(thumb, FormatJPEG, thumbnailQuality)
	if err != nil {
		t.Fatal(err)
	}
	at75, err := encode(thumb, FormatJPEG, defaultQuality)
	if err != nil {
		t.Fatal(err)
	}
	if len(at95) <= len(at75) {
		t.Errorf("q95 thumbnail (%d bytes) is not larger than q75 (%d bytes)", len(at95), len(at75))
	}
	if thumbnailQuality != 95 || defaultQuality != 75 {
		t.Errorf("quality constants drifted: thumbnail=%d default=%d, want 95 and 75",
			thumbnailQuality, defaultQuality)
	}
}

// TestCoverCropGeometry checks the crop matches bimg.Thumbnail's arithmetic,
// including its no-enlarge guard being an AND.
func TestCoverCropGeometry(t *testing.T) {
	tests := []struct {
		name         string
		w, h, size   int
		wantW, wantH int
	}{
		{"landscape", 400, 300, 128, 128, 128},
		{"portrait", 300, 400, 128, 128, 128},
		{"square", 500, 500, 128, 128, 128},
		{"already square and small", 64, 64, 128, 64, 64},
		// One side smaller than the target but not both: bimg's guard is an
		// AND, so this still scales up before cropping.
		{"tall and narrow", 100, 2000, 128, 128, 128},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := coverCrop(synthImage(tc.w, tc.h), tc.size)
			if got.Bounds().Dx() != tc.wantW || got.Bounds().Dy() != tc.wantH {
				t.Errorf("coverCrop(%dx%d, %d) = %dx%d, want %dx%d",
					tc.w, tc.h, tc.size, got.Bounds().Dx(), got.Bounds().Dy(), tc.wantW, tc.wantH)
			}
		})
	}
}

// TestTransparentPNGFlattensToWhite pins the one deliberate behaviour change:
// libvips dropped the alpha band and Go would premultiply it to black, so a
// transparent region must be composited onto white instead.
func TestTransparentPNGFlattensToWhite(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 64, 64))
	// Left half fully transparent, right half opaque red.
	for y := 0; y < 64; y++ {
		for x := 32; x < 64; x++ {
			src.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	decoded, _, err := decodeImage(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	flat := flatten(decoded)
	r, g, b, a := flat.At(8, 8).RGBA()
	if r>>8 < 250 || g>>8 < 250 || b>>8 < 250 || a>>8 != 255 {
		t.Errorf("transparent region became rgba(%d,%d,%d,%d), want opaque white",
			r>>8, g>>8, b>>8, a>>8)
	}
	// The opaque half must be untouched.
	r2, g2, b2, _ := flat.At(48, 8).RGBA()
	if r2>>8 < 250 || g2>>8 > 5 || b2>>8 > 5 {
		t.Errorf("opaque region became rgba(%d,%d,%d), want red", r2>>8, g2>>8, b2>>8)
	}
}

func TestProcessImageRejectsEmptyConfig(t *testing.T) {
	src := synthImage(100, 100)
	if _, err := processImage(src, nil, getDefaultDims(), 128); err == nil {
		t.Error("expected an error with no formats")
	}
	if _, err := processImage(src, getDefaultImageTypes(), nil, 128); err == nil {
		t.Error("expected an error with no dims")
	}
}

// TestProcessRealHEIC runs the full pipeline on the fixtures that motivated
// HEIC support in the first place.
func TestProcessRealHEIC(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.heic"))
	if len(files) == 0 {
		t.Skip("no heic fixtures")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			img, _, err := decodeImage(data)
			if err != nil {
				t.Fatal(err)
			}
			derivatives, err := processImage(img, getDefaultImageTypes(), getDefaultDims(), defaultThumbSize)
			if err != nil {
				t.Fatal(err)
			}
			if len(derivatives) != 14 {
				t.Errorf("got %d derivatives, want 14", len(derivatives))
			}
			for _, d := range derivatives {
				if _, format := decodeDerivative(t, d); format != d.Format.String() {
					t.Errorf("%s: got %s", d.Filename(), format)
				}
			}
		})
	}
}

// TestOversizedInputRejected uses a hand-built PNG header claiming a canvas
// beyond maxPixels, so nothing large is ever allocated.
func TestOversizedInputRejected(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	// IHDR width/height are the 4-byte big-endian values at offsets 16 and 20.
	for i, v := range []byte{0x00, 0x01, 0x00, 0x00} { // 65536
		data[16+i] = v
		data[20+i] = v
	}
	if _, _, err := decodeImage(data); err == nil {
		t.Error("a 65536x65536 image was accepted, want rejection")
	}
}

func TestJPEGQualityConstantsMatchLegacy(t *testing.T) {
	// bimg.Quality was 75 and bimg.Thumbnail hardcoded 95.
	if defaultQuality != 75 {
		t.Errorf("defaultQuality = %d, want 75 to match the previous libvips output", defaultQuality)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, synthImage(32, 32), &jpeg.Options{Quality: defaultQuality}); err != nil {
		t.Fatal(err)
	}
}
