package main

import (
	"fmt"
	"image"
)

// processImage builds the full derivative set from a decoded image.
//
// It returns the results in memory rather than writing files. That removes
// /tmp from the hot path entirely, which is what fixes the stale-output bug
// (a warm Lambda container used to re-upload the previous invocation's files
// under the new key prefix) and the leaked file descriptors along with it.
//
// The pipeline mirrors what libvips did internally:
//
//  1. decode once (done by the caller) and flatten any alpha onto white
//  2. encode orig.jpeg at the original dimensions
//  3. walk the breakpoints largest-first, feeding each resize into the next
//  4. centre-crop a square thumbnail
//
// Chaining is both faster and higher quality than the previous code, which
// re-decoded orig.jpeg for every derivative and so stacked a fresh generation
// of JPEG loss onto each one. Here the chain runs on raw pixels.
func processImage(
	src image.Image,
	formats []ImageFormat,
	dims map[string]ImageSize,
	thumbSize int,
) ([]Derivative, error) {
	if len(formats) == 0 {
		return nil, fmt.Errorf("no output formats requested")
	}
	if len(dims) == 0 {
		return nil, fmt.Errorf("no output dimensions requested")
	}

	// One flatten for the whole run. Every derivative descends from this, just
	// as every derivative used to descend from orig.jpeg.
	base := flatten(src)
	origDims := ImageSize{Width: base.Bounds().Dx(), Height: base.Bounds().Dy()}
	if origDims.Width == 0 || origDims.Height == 0 {
		return nil, fmt.Errorf("image has zero dimension: %dx%d", origDims.Width, origDims.Height)
	}

	out := make([]Derivative, 0, len(dims)*len(formats)+2)

	origData, err := encode(base, FormatJPEG, defaultQuality)
	if err != nil {
		return nil, fmt.Errorf("orig: %w", err)
	}
	out = append(out, Derivative{Name: "orig", Format: FormatJPEG, Data: origData})

	// Descending order: each stage is the source for the next.
	var cur image.Image = base
	curDims := origDims
	var thumbSource image.Image = base

	for _, ns := range sortedDims(dims) {
		// Computed against the ORIGINAL dimensions, not the current chain
		// stage, so "never upscale" behaves exactly as it always has.
		newDims := smartDims(origDims, ns.Box)

		if newDims != curDims {
			cur = resizeTo(cur, newDims.Width, newDims.Height)
			curDims = newDims
		}

		for _, format := range formats {
			data, err := encode(cur, format, defaultQuality)
			if err != nil {
				return nil, fmt.Errorf("%s.%v: %w", ns.Name, format, err)
			}
			out = append(out, Derivative{Name: ns.Name, Format: format, Data: data})
		}

		// Crop the thumbnail from the smallest stage still comfortably larger
		// than it, rather than from the full-size original.
		if curDims.Width >= thumbSize*2 && curDims.Height >= thumbSize*2 {
			thumbSource = cur
		}
	}

	thumb := coverCrop(thumbSource, thumbSize)
	thumbData, err := encode(thumb, FormatJPEG, thumbnailQuality)
	if err != nil {
		return nil, fmt.Errorf("thumbnail: %w", err)
	}
	out = append(out, Derivative{Name: "thumbnail", Format: FormatJPEG, Data: thumbData})

	return out, nil
}
