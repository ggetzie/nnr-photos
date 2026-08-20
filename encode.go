package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	"github.com/gen2brain/webp"
)

// Quality defaults. bimg used 75 for everything (bimg.Quality) except
// Thumbnail, which hardcodes 95 -- so thumbnail.jpeg has always been noticeably
// higher quality than the other derivatives. Preserved deliberately.
const (
	defaultQuality   = 75
	thumbnailQuality = 95

	// webpMethod is libwebp's quality/speed trade-off (0 fast .. 6 best).
	// 4 is libwebp's own default.
	webpMethod = 4
)

// encode renders img in the requested format. Callers are expected to have
// flattened any alpha already (processImage does this once, right after decode).
//
// This function is the swap seam for the encoder stack: replacing
// gen2brain/webp with another CGo-free encoder means changing only this file.
func encode(img image.Image, format ImageFormat, quality int) ([]byte, error) {
	var buf bytes.Buffer
	switch format {
	case FormatJPEG:
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, fmt.Errorf("encoding jpeg: %w", err)
		}
	case FormatWEBP:
		if err := webp.Encode(&buf, img, webp.Options{Quality: quality, Method: webpMethod}); err != nil {
			return nil, fmt.Errorf("encoding webp: %w", err)
		}
	case FormatPNG:
		if err := png.Encode(&buf, img); err != nil {
			return nil, fmt.Errorf("encoding png: %w", err)
		}
	default:
		return nil, fmt.Errorf("cannot encode format %v", format)
	}
	return buf.Bytes(), nil
}
