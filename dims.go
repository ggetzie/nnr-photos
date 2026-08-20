package main

import (
	"fmt"
	"path"
	"strconv"
	"strings"
)

// getDefaultDims maps CSS breakpoint names to the maximum box for that
// breakpoint. The keys become the output filenames and are mirrored in the
// Django app as recipes.models.SCREEN_SIZES -- keep the two in sync.
func getDefaultDims() map[string]ImageSize {
	return map[string]ImageSize{
		"1200": {Width: 1090, Height: 818},
		"992":  {Width: 910, Height: 683},
		"768":  {Width: 670, Height: 503},
		"576":  {Width: 515, Height: 386},
		"408":  {Width: 400, Height: 300},
		"320":  {Width: 310, Height: 225},
	}
}

func getDefaultImageTypes() []ImageFormat {
	return []ImageFormat{FormatJPEG, FormatWEBP}
}

// parseDims takes a string in the format name1:Width1,Height1;name2:Width2,Height2
// and converts it to a map of name -> ImageSize.
func parseDims(dimStr string) (map[string]ImageSize, error) {
	if dimStr == "" {
		// os.Getenv returns an empty string when the variable is not defined,
		// so empty means "not configured" and we fall back to the defaults.
		return getDefaultDims(), nil
	}

	res := make(map[string]ImageSize)
	for _, spec := range strings.Split(dimStr, ";") {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		name, sizes, found := strings.Cut(spec, ":")
		if !found {
			return nil, fmt.Errorf("invalid dimension %q: expected name:width,height", spec)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("invalid dimension %q: empty name", spec)
		}
		wStr, hStr, found := strings.Cut(sizes, ",")
		if !found {
			return nil, fmt.Errorf("invalid dimension %q: expected width,height", spec)
		}
		width, err := strconv.Atoi(strings.TrimSpace(wStr))
		if err != nil {
			return nil, fmt.Errorf("invalid width in %q: %w", spec, err)
		}
		height, err := strconv.Atoi(strings.TrimSpace(hStr))
		if err != nil {
			return nil, fmt.Errorf("invalid height in %q: %w", spec, err)
		}
		if width <= 0 || height <= 0 {
			return nil, fmt.Errorf("invalid dimension %q: width and height must be positive", spec)
		}
		res[name] = ImageSize{Width: width, Height: height}
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("no valid dimensions in %q", dimStr)
	}
	return res, nil
}

// parseImageTypes takes a comma separated list of extensions and returns the
// output formats, e.g. "jpeg,webp" -> [FormatJPEG, FormatWEBP].
func parseImageTypes(formats string) ([]ImageFormat, error) {
	if formats == "" {
		return getDefaultImageTypes(), nil
	}
	var res []ImageFormat
	for _, ext := range strings.Split(formats, ",") {
		ext = strings.TrimSpace(strings.ToLower(ext))
		if ext == "" {
			continue
		}
		t, err := getImageType(ext)
		if err != nil {
			return nil, err
		}
		res = append(res, t)
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("no valid output formats in %q", formats)
	}
	return res, nil
}

func buildPath(folder string, name string, iType ImageFormat) string {
	filename := fmt.Sprintf("%s.%v", name, iType)
	return path.Join(folder, filename)
}

// resizeToHeight adjusts dimensions to match height, preserving aspect ratio.
func resizeToHeight(originalDims ImageSize, height int) ImageSize {
	newWidth := originalDims.Width * height / originalDims.Height
	return ImageSize{Width: newWidth, Height: height}
}

// resizeToWidth adjusts dimensions to match width, preserving aspect ratio.
func resizeToWidth(originalDims ImageSize, width int) ImageSize {
	newHeight := originalDims.Height * width / originalDims.Width
	return ImageSize{Width: width, Height: newHeight}
}

// smartDims calculates dimensions that fit within the max width and height
// while preserving aspect ratio. It never upscales: an image already smaller
// than the box is returned unchanged.
func smartDims(originalDims ImageSize, maxDims ImageSize) ImageSize {
	if (originalDims.Width <= maxDims.Width) && (originalDims.Height <= maxDims.Height) {
		// already small enough
		return originalDims
	}
	var resized ImageSize
	if originalDims.Width > originalDims.Height {
		// Landscape - more wide than tall
		resized = resizeToWidth(originalDims, maxDims.Width)
		if resized.Height > maxDims.Height {
			// Width correct, but still too tall
			resized = resizeToHeight(resized, maxDims.Height)
		}
	} else {
		// Portrait - more tall than wide
		resized = resizeToHeight(originalDims, maxDims.Height)
		if resized.Width > maxDims.Width {
			// height correct but still too wide
			resized = resizeToWidth(resized, maxDims.Width)
		}
	}
	return resized
}
