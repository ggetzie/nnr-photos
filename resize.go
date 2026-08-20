package main

import (
	"image"
	"image/color"
	"math"

	"golang.org/x/image/draw"
)

// resampler is the kernel used for the final, quality-critical scale.
// draw.Kernel is exported, so swapping in Lanczos3 here is a one-line change.
var resampler = draw.CatmullRom

// preShrinkThreshold is how much larger than the target the source may be
// before we do a cheap pre-shrink pass first. CatmullRom's cost scales with the
// source area, so shrinking a 12MP image straight to 1090px wide costs ~460ms
// while a bilinear pre-pass plus CatmullRom costs ~200ms for the same result.
const preShrinkThreshold = 2.0

// resizeTo scales src to exactly w x h.
//
// It always returns *image.RGBA and always uses draw.Src: x/image/draw only has
// generated fast paths for those, and falling back to the generic interface
// path is roughly an order of magnitude slower.
func resizeTo(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	if w <= 0 || h <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 0, 0))
	}

	// Cheap bilinear pre-shrink when the source is far larger than the target.
	if ratio := math.Min(float64(b.Dx())/float64(w), float64(b.Dy())/float64(h)); ratio > preShrinkThreshold {
		iw := int(float64(b.Dx()) / ratio * preShrinkThreshold)
		ih := int(float64(b.Dy()) / ratio * preShrinkThreshold)
		if iw > w && ih > h {
			mid := image.NewRGBA(image.Rect(0, 0, iw, ih))
			draw.ApproxBiLinear.Scale(mid, mid.Bounds(), src, b, draw.Src, nil)
			src, b = mid, mid.Bounds()
		}
	}

	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	resampler.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// flatten composites src onto an opaque white background.
//
// This is a deliberate behaviour change. libvips never flattened here (bimg's
// imageFlatten returns early on the default black background) and simply
// dropped the alpha band. Go's image/jpeg encoder reads alpha-premultiplied
// values, so a transparent region would encode as black. White is what a
// transparent PNG logo should become on a recipe page.
//
// If src is already opaque this is a straight copy.
func flatten(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))

	if op, ok := src.(interface{ Opaque() bool }); ok && op.Opaque() {
		draw.Copy(dst, image.Point{}, src, b, draw.Src, nil)
		return dst
	}

	// draw.Over onto a white canvas performs the composite for us.
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Over)
	return dst
}

// coverCrop scales src so the shorter side reaches size, then takes a centred
// size x size square.
//
// The arithmetic deliberately mirrors bimg.Thumbnail (Options{Width: size,
// Height: size, Crop: true}), including its no-enlarge guard being an AND --
// so a 100x2000 image really does get upscaled before cropping.
func coverCrop(src image.Image, size int) *image.RGBA {
	b := src.Bounds()
	inW, inH := b.Dx(), b.Dy()
	if size <= 0 || inW == 0 || inH == 0 {
		return image.NewRGBA(image.Rect(0, 0, 0, 0))
	}

	factor := math.Min(float64(inW)/float64(size), float64(inH)/float64(size))
	if inW < size && inH < size {
		factor = 1
	}
	scaledW := roundFloat(float64(inW) / factor)
	scaledH := roundFloat(float64(inH) / factor)

	scaled := resizeTo(src, scaledW, scaledH)

	left := (scaledW - size + 1) / 2
	top := (scaledH - size + 1) / 2
	if left < 0 {
		left = 0
	}
	if top < 0 {
		top = 0
	}
	cropW, cropH := min(size, scaledW), min(size, scaledH)

	dst := image.NewRGBA(image.Rect(0, 0, cropW, cropH))
	draw.Copy(dst, image.Point{}, scaled, image.Rect(left, top, left+cropW, top+cropH), draw.Src, nil)
	return dst
}

// roundFloat matches libvips' rounding (floor(x + 0.5)).
func roundFloat(f float64) int {
	return int(math.Floor(f + 0.5))
}
