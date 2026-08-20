package main

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

// TestOrientation decodes each of the eight EXIF orientation variants and
// asserts the result matches the already-upright reference. These fixtures are
// the standard "F" images, so a transpose/mirror mixup that dimension checks
// would miss shows up here.
func TestOrientation(t *testing.T) {
	for _, kind := range []string{"landscape", "portrait"} {
		refPath := filepath.Join("testdata", "orientation", kind+"_1.jpg")
		refData, err := os.ReadFile(refPath)
		if err != nil {
			t.Skipf("fixture %s missing: %v", refPath, err)
		}
		ref, _, err := decodeImage(refData)
		if err != nil {
			t.Fatalf("%s: %v", refPath, err)
		}

		for i := 2; i <= 8; i++ {
			path := filepath.Join("testdata", "orientation", kind+"_"+string(rune('0'+i))+".jpg")
			t.Run(filepath.Base(path), func(t *testing.T) {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Skipf("fixture missing: %v", err)
				}
				got, _, err := decodeImage(data)
				if err != nil {
					t.Fatal(err)
				}
				if got.Bounds().Size() != ref.Bounds().Size() {
					t.Fatalf("size %v, want %v (orientation not applied?)", got.Bounds().Size(), ref.Bounds().Size())
				}
				if d := meanAbsDiff(got, ref); d > 12 {
					t.Errorf("mean abs diff from upright reference = %.1f/255, want <= 12", d)
				}
			})
		}
	}
}

// TestOrientationTagParsing checks the raw tag reader against the fixtures.
func TestOrientationTagParsing(t *testing.T) {
	for i := 1; i <= 8; i++ {
		path := filepath.Join("testdata", "orientation", "landscape_"+string(rune('0'+i))+".jpg")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Skipf("fixture missing: %v", err)
		}
		if got := readOrientation(data, "jpeg"); got != i {
			t.Errorf("%s: readOrientation = %d, want %d", filepath.Base(path), got, i)
		}
	}
}

// TestReadOrientationNeverFails: malformed input must yield 1, not an error or
// a panic. A corrupt metadata block must not fail the job.
func TestReadOrientationNeverFails(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0xFF},
		{0xFF, 0xD8},
		{0xFF, 0xD8, 0xFF, 0xE1},
		{0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF},
		{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x08, 'E', 'x', 'i', 'f', 0x00, 0x00},
		append([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x20, 'E', 'x', 'i', 'f', 0, 0, 'I', 'I'}, bytes.Repeat([]byte{0xFF}, 32)...),
		[]byte("not a jpeg at all"),
	}
	for i, data := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("case %d panicked: %v", i, r)
				}
			}()
			if got := readOrientation(data, "jpeg"); got != 1 {
				t.Errorf("case %d: got %d, want 1", i, got)
			}
		}()
	}
}

func TestApplyOrientationDimensions(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for _, tc := range []struct {
		o     int
		wantW int
		wantH int
	}{
		{1, 40, 20}, {2, 40, 20}, {3, 40, 20}, {4, 40, 20},
		{5, 20, 40}, {6, 20, 40}, {7, 20, 40}, {8, 20, 40},
	} {
		got := applyOrientation(src, tc.o)
		if got.Bounds().Dx() != tc.wantW || got.Bounds().Dy() != tc.wantH {
			t.Errorf("orientation %d: %dx%d, want %dx%d", tc.o,
				got.Bounds().Dx(), got.Bounds().Dy(), tc.wantW, tc.wantH)
		}
	}
	// Out-of-range values are ignored rather than corrupting the image.
	for _, o := range []int{0, -1, 9, 99} {
		if got := applyOrientation(src, o); got != image.Image(src) {
			t.Errorf("orientation %d should be a no-op", o)
		}
	}
}

// TestDecodeHEIC covers the format that motivated this work. test.heic is a
// grid/tiled image, which is how iPhones store photos.
func TestDecodeHEIC(t *testing.T) {
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
			img, format, err := decodeImage(data)
			if err != nil {
				t.Fatalf("decodeImage: %v", err)
			}
			if format != "heic" {
				t.Errorf("format = %q, want heic", format)
			}
			b := img.Bounds()
			if b.Dx() == 0 || b.Dy() == 0 {
				t.Fatal("zero-size image")
			}
			if uniformColors(img) < 5 {
				t.Error("decoded image looks blank - decoder likely broken")
			}
			t.Logf("%s: %dx%d", filepath.Base(f), b.Dx(), b.Dy())
		})
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("this is not an image"),
		{},
		bytes.Repeat([]byte{0}, 1024),
	} {
		if _, _, err := decodeImage(data); err == nil {
			t.Errorf("decodeImage(%d bytes of garbage) succeeded, want error", len(data))
		}
	}
}

// meanAbsDiff is the mean absolute per-channel difference between two images of
// equal size, in 0-255 units.
func meanAbsDiff(a, b image.Image) float64 {
	ab, bb := a.Bounds(), b.Bounds()
	if ab.Size() != bb.Size() {
		return 255
	}
	var total, n float64
	for y := 0; y < ab.Dy(); y++ {
		for x := 0; x < ab.Dx(); x++ {
			ar, ag, aa, _ := a.At(ab.Min.X+x, ab.Min.Y+y).RGBA()
			br, bg, bb2, _ := b.At(bb.Min.X+x, bb.Min.Y+y).RGBA()
			total += absDiff(ar, br) + absDiff(ag, bg) + absDiff(aa, bb2)
			n += 3
		}
	}
	return total / n / 257
}

func absDiff(a, b uint32) float64 {
	if a > b {
		return float64(a - b)
	}
	return float64(b - a)
}

func uniformColors(img image.Image) int {
	b := img.Bounds()
	seen := map[color.RGBA]bool{}
	for y := b.Min.Y; y < b.Max.Y; y += b.Dy()/17 + 1 {
		for x := b.Min.X; x < b.Max.X; x += b.Dx()/17 + 1 {
			r, g, bl, a := img.At(x, y).RGBA()
			seen[color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), uint8(a >> 8)}] = true
		}
	}
	return len(seen)
}
