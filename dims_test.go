package main

import (
	"reflect"
	"testing"
)

// TestSmartDims pins the exact integer output for every default breakpoint.
// These numbers are the contract with the Django <picture>/<source> markup
// (recipes.models.SCREEN_SIZES), so they must not drift.
func TestSmartDims(t *testing.T) {
	tests := []struct {
		name string
		orig ImageSize
		box  ImageSize
		want ImageSize
	}{
		// Landscape 4:3, larger than every box.
		{"4:3 landscape -> 1200", ImageSize{4000, 3000}, ImageSize{1090, 818}, ImageSize{1090, 817}},
		{"4:3 landscape -> 992", ImageSize{4000, 3000}, ImageSize{910, 683}, ImageSize{910, 682}},
		{"4:3 landscape -> 768", ImageSize{4000, 3000}, ImageSize{670, 503}, ImageSize{670, 502}},
		{"4:3 landscape -> 576", ImageSize{4000, 3000}, ImageSize{515, 386}, ImageSize{515, 386}},
		{"4:3 landscape -> 408", ImageSize{4000, 3000}, ImageSize{400, 300}, ImageSize{400, 300}},
		{"4:3 landscape -> 320", ImageSize{4000, 3000}, ImageSize{310, 225}, ImageSize{300, 225}},

		// Portrait 3:4.
		{"3:4 portrait -> 1200", ImageSize{3000, 4000}, ImageSize{1090, 818}, ImageSize{613, 818}},
		{"3:4 portrait -> 320", ImageSize{3000, 4000}, ImageSize{310, 225}, ImageSize{168, 225}},

		// Square.
		{"square -> 1200", ImageSize{2000, 2000}, ImageSize{1090, 818}, ImageSize{818, 818}},

		// Very wide panorama: width fits first, height already inside.
		{"panorama -> 1200", ImageSize{6000, 1000}, ImageSize{1090, 818}, ImageSize{1090, 181}},

		// Never upscales.
		{"already small", ImageSize{200, 150}, ImageSize{1090, 818}, ImageSize{200, 150}},
		{"exactly the box", ImageSize{1090, 818}, ImageSize{1090, 818}, ImageSize{1090, 818}},
		{"one px over on width", ImageSize{1091, 818}, ImageSize{1090, 818}, ImageSize{1090, 817}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := smartDims(tc.orig, tc.box)
			if got != tc.want {
				t.Errorf("smartDims(%v, %v) = %v, want %v", tc.orig, tc.box, got, tc.want)
			}
			// Whatever comes out must fit inside the box.
			if got.Width > tc.box.Width || got.Height > tc.box.Height {
				if tc.orig.Width > tc.box.Width || tc.orig.Height > tc.box.Height {
					t.Errorf("result %v exceeds box %v", got, tc.box)
				}
			}
		})
	}
}

// TestSmartDimsNeverUpscales sweeps every default box against small inputs.
func TestSmartDimsNeverUpscales(t *testing.T) {
	small := ImageSize{200, 150}
	for name, box := range getDefaultDims() {
		if got := smartDims(small, box); got != small {
			t.Errorf("breakpoint %s: smartDims(%v, %v) = %v, want unchanged", name, small, box, got)
		}
	}
}

func TestBuildPath(t *testing.T) {
	tests := []struct {
		folder, name string
		format       ImageFormat
		want         string
	}{
		{"/media/images/recipes/3", "1040", FormatJPEG, "/media/images/recipes/3/1040.jpeg"},
		{"/media/images/recipes/3", "1040", FormatWEBP, "/media/images/recipes/3/1040.webp"},
		{"out", "thumbnail", FormatJPEG, "out/thumbnail.jpeg"},
	}
	for _, tc := range tests {
		if got := buildPath(tc.folder, tc.name, tc.format); got != tc.want {
			t.Errorf("buildPath(%q, %q, %v) = %q, want %q", tc.folder, tc.name, tc.format, got, tc.want)
		}
	}
}

func TestParseDims(t *testing.T) {
	t.Run("empty falls back to defaults", func(t *testing.T) {
		got, err := parseDims("")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, getDefaultDims()) {
			t.Errorf("got %v, want defaults", got)
		}
	})
	t.Run("valid", func(t *testing.T) {
		got, err := parseDims("web:800,600;mobile:400,300")
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]ImageSize{"web": {800, 600}, "mobile": {400, 300}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	for _, bad := range []string{"web", "web:800", "web:800,abc", "web:0,600", "web:-1,600", ":800,600"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			if _, err := parseDims(bad); err == nil {
				t.Errorf("parseDims(%q) succeeded, want error", bad)
			}
		})
	}
}

func TestParseImageTypes(t *testing.T) {
	t.Run("empty falls back to jpeg+webp", func(t *testing.T) {
		got, err := parseImageTypes("")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, []ImageFormat{FormatJPEG, FormatWEBP}) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("jpg is an alias for jpeg", func(t *testing.T) {
		got, err := parseImageTypes("jpg")
		if err != nil || len(got) != 1 || got[0] != FormatJPEG {
			t.Errorf("got %v, %v", got, err)
		}
	})
	// The old implementation accepted these and then silently produced nothing.
	for _, bad := range []string{"tiff", "gif", "pdf", "svg", "magick", "heif", "avif", "bogus"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			if _, err := parseImageTypes(bad); err == nil {
				t.Errorf("parseImageTypes(%q) succeeded, want error", bad)
			}
		})
	}
}

// TestSortedDims guards the ordering the resize chain depends on.
func TestSortedDims(t *testing.T) {
	got := sortedDims(getDefaultDims())
	want := []string{"1200", "992", "768", "576", "408", "320"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i, ns := range got {
		if ns.Name != want[i] {
			t.Errorf("position %d: got %q, want %q", i, ns.Name, want[i])
		}
	}
	// Deterministic across runs despite map iteration order.
	for i := 0; i < 50; i++ {
		again := sortedDims(getDefaultDims())
		if !reflect.DeepEqual(got, again) {
			t.Fatal("sortedDims is not deterministic")
		}
	}
}
