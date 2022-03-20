package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"testing"

	"github.com/h2non/bimg"
)

func TestPrintMetadata(t *testing.T) {
	printMetadata("/media/gabe/data/pictures/websites/nnr/test/1/orig.jpg")
}

func TestBuildPath(t *testing.T) {
	want := "/media/images/recipes/3/1040.jpeg"
	result := buildPath("/media/images/recipes/3", "1040", bimg.JPEG)
	if result != want {
		t.Fatalf(`buildPath Failed. Wanted %q Got %v`, want, result)
	}
}

func TestProcessImage(t *testing.T) {
	folder := "/usr/local/src/nnr/nnr/media/images/tags/breakfast/"
	file := "orig.jpeg"
	filepath := path.Join(folder, file)
	img, err := loadImageLocal(filepath)
	if err != nil {
		t.Fatalf(`failed to load test image: %v`, filepath)
	}
	iTypes := getDefaultImageTypes()
	dims := getDefaultDims()
	processImage(img, iTypes, dims, folder)
	for screenSize := range dims {
		for _, iType := range iTypes {
			filename := fmt.Sprintf("%s.%s", screenSize, bimg.ImageTypeName(iType))
			filepath := path.Join(folder, filename)
			_, err := os.Stat(filepath)
			if errors.Is(err, os.ErrNotExist) {
				t.Fatalf(`Could not find output: %v`, filepath)
			}
		}

	}
}

func TestInvalid(t *testing.T) {
	filepath := "/media/gabe/data/pictures/websites/nnr/test/project.css"
	img, err := loadImageLocal(filepath)
	if err != nil {
		t.Fatalf("Failed to load %s, %v", filepath, err)
	}
	iType := img.Type()
	t.Logf("Image Type Supported? %t", bimg.IsTypeNameSupported(iType))

}
