package main

import (
	"fmt"
	"sort"
)

// ImageSize is a width/height pair. It replaces bimg.ImageSize so that no
// libvips-backed type leaks into the pure geometry helpers.
type ImageSize struct {
	Width  int
	Height int
}

// ImageFormat enumerates the formats this program can *write*. Input formats
// are handled separately in decode.go and are a much longer list.
type ImageFormat int

const (
	FormatUnknown ImageFormat = iota
	FormatJPEG
	FormatWEBP
	FormatPNG
)

// String returns the file extension for the format. buildPath formats an
// ImageFormat with %v, so this is what produces "1200.jpeg".
func (f ImageFormat) String() string {
	switch f {
	case FormatJPEG:
		return "jpeg"
	case FormatWEBP:
		return "webp"
	case FormatPNG:
		return "png"
	}
	return "unknown"
}

// ContentType is the MIME type to set on the S3 object. Without this S3 serves
// the derivatives as binary/octet-stream.
func (f ImageFormat) ContentType() string {
	switch f {
	case FormatJPEG:
		return "image/jpeg"
	case FormatWEBP:
		return "image/webp"
	case FormatPNG:
		return "image/png"
	}
	return "application/octet-stream"
}

// getImageType maps an extension from FORMATS/--formats to an output format.
// The previous implementation also accepted tiff, gif, pdf, svg, magick, heif
// and avif; those were transcribed from bimg's enum rather than being real
// requirements, and most of them are not meaningful as outputs.
func getImageType(ext string) (ImageFormat, error) {
	switch ext {
	case "jpeg", "jpg":
		return FormatJPEG, nil
	case "webp":
		return FormatWEBP, nil
	case "png":
		return FormatPNG, nil
	}
	return FormatUnknown, fmt.Errorf("unsupported output format: %q (supported: jpeg, webp, png)", ext)
}

// Derivative is one generated image held in memory. processImage returns these
// instead of writing files, which is what lets Handler skip /tmp entirely.
type Derivative struct {
	Name   string // "orig", "thumbnail", "1200", ...
	Format ImageFormat
	Data   []byte
}

// Filename is the object/file name for this derivative, e.g. "1200.webp".
func (d Derivative) Filename() string {
	return fmt.Sprintf("%s.%v", d.Name, d.Format)
}

// ContentType is the MIME type for this derivative.
func (d Derivative) ContentType() string {
	return d.Format.ContentType()
}

// namedSize pairs a breakpoint name with its maximum box.
type namedSize struct {
	Name string
	Box  ImageSize
}

// sortedDims orders the dimension map largest first. Map iteration order is
// random, and the resize chain in processImage feeds each stage into the next,
// so it must run largest to smallest to be both correct and cheap.
func sortedDims(dims map[string]ImageSize) []namedSize {
	out := make([]namedSize, 0, len(dims))
	for name, box := range dims {
		out = append(out, namedSize{Name: name, Box: box})
	}
	sort.Slice(out, func(i, j int) bool {
		ai := out[i].Box.Width * out[i].Box.Height
		aj := out[j].Box.Width * out[j].Box.Height
		if ai != aj {
			return ai > aj
		}
		return out[i].Name < out[j].Name // stable for equal areas
	})
	return out
}
