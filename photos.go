package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/h2non/bimg"
)

func loadImageLocal(filepath string) (*bimg.Image, error) {
	buffer, err := bimg.Read(filepath)
	if err != nil {
		return nil, err
	}
	return bimg.NewImage(buffer), nil
}

func saveImageLocal(buffer []byte, filepath string) {
	bimg.Write(filepath, buffer)
}

func getDefaultImageTypes() []bimg.ImageType {
	return []bimg.ImageType{bimg.JPEG, bimg.WEBP}
}

func getImageType(ext string) (bimg.ImageType, error) {
	switch ext {
	case "jpeg", "jpg":
		return bimg.JPEG, nil
	case "png":
		return bimg.PNG, nil
	case "webp":
		return bimg.WEBP, nil
	case "tiff":
		return bimg.TIFF, nil
	case "gif":
		return bimg.GIF, nil
	case "pdf":
		return bimg.PDF, nil
	case "svg":
		return bimg.SVG, nil
	case "magick":
		return bimg.MAGICK, nil
	case "heif":
		return bimg.HEIF, nil
	case "avif":
		return bimg.AVIF, nil
	}

	return bimg.UNKNOWN, fmt.Errorf("unknown image type: %s", ext)
}

func parseImageTypes(formats string) ([]bimg.ImageType, error) {
	// take a string of image type extensions and return the bimg.ImageType values
	// e.g. "jpg,png,webp" -> [bimg.JPEG, bimg.PNG, bimg.WEBP]
	if formats == "" {
		// os.Getenv will return an empty string if the variable is not defined
		// use default values in this case.
		return getDefaultImageTypes(), nil
	}
	imageTypeStrings := strings.Split(formats, ",")
	var res []bimg.ImageType

	for _, ext := range imageTypeStrings {
		t, err := getImageType(strings.ToLower(ext))
		if err != nil {
			return res, err
		}
		res = append(res, t)
	}
	return res, nil
}

func getDefaultDims() map[string]bimg.ImageSize {
	// Array of {width, height} to resize photos to
	dims := map[string]bimg.ImageSize{
		"1200": {Width: 1090, Height: 818},
		"992":  {Width: 910, Height: 683},
		"768":  {Width: 670, Height: 503},
		"576":  {Width: 515, Height: 386},
		"408":  {Width: 400, Height: 300},
		"320":  {Width: 310, Height: 225},
	}
	return dims
}

func parseDims(dimStr string) (map[string]bimg.ImageSize, error) {
	// Take string in the format name1:Width1,Height1;name2:Width2,Height2...
	// and convert to map of
	// name1: {Width: Width1, Height: Height1}
	// name2: {Width: Width2, Height: Height2}
	// ...
	if dimStr == "" {
		// os.Getenv will return an empty string if the variable is not defined
		// use default values in this case.
		return getDefaultDims(), nil
	}
	dimStrs := strings.Split(dimStr, ";") // ["name1:width1,height1", "name2:width2,height2"]
	dims := make(map[string]bimg.ImageSize)

	for _, ds := range dimStrs {
		nameWHs := strings.Split(ds, ":") // ["name", "width,height"]
		if len(nameWHs) != 2 {
			return dims, errors.New("Invalid dimensions format: " + ds)
		}
		name := nameWHs[0]
		wh := strings.Split(nameWHs[1], ",") // ["width", "height"]
		if len(wh) != 2 {
			return dims, errors.New("Invalid dimensions format: " + ds)
		}
		width, err := strconv.Atoi(wh[0])
		if err != nil {
			return dims, fmt.Errorf("invalid width value %s in dim string: %s", wh[0], ds)
		}
		height, err := strconv.Atoi(wh[1])
		if err != nil {
			return dims, fmt.Errorf("invalid height value %s in dim string %s", wh[1], ds)
		}
		dims[name] = bimg.ImageSize{Width: width, Height: height}
	}

	return dims, nil
}

func printMetadata(filepath string) {
	buffer, err := bimg.Read(filepath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	metadata, err := bimg.Metadata(buffer)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	fmt.Printf("metadata\n%+v", metadata)
}

func buildPath(folder string, name string, iType bimg.ImageType) string {
	filename := fmt.Sprintf("%s.%v", name, bimg.ImageTypeName(iType))
	return path.Join(folder, filename)
}

func resizeToHeight(originalDims bimg.ImageSize, height int) bimg.ImageSize {
	// adjust dimensions to match height, preserving aspect ratio
	newWidth := originalDims.Width * height / originalDims.Height
	return bimg.ImageSize{Width: newWidth, Height: height}
}

func resizeToWidth(originalDims bimg.ImageSize, width int) bimg.ImageSize {
	// adjust dimensions to match width, preserving aspect ratio
	newHeight := originalDims.Height * width / originalDims.Width
	return bimg.ImageSize{Width: width, Height: newHeight}
}

func smartDims(originalDims bimg.ImageSize, maxDims bimg.ImageSize) bimg.ImageSize {
	// Calculate dims within max width and height, preserving aspect ratio
	if (originalDims.Width <= maxDims.Width) && (originalDims.Height <= maxDims.Height) {
		// already small enough
		return originalDims
	}
	var resized bimg.ImageSize
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

func processImage(
	img *bimg.Image,
	imageTypes []bimg.ImageType,
	dims map[string]bimg.ImageSize,
	save_to string,
	thumbSize int) (string, error) {
	origOptions := bimg.Options{
		NoAutoRotate:  false,
		StripMetadata: true,
		Type:          bimg.JPEG,
	}
	origImage, err := img.Process(origOptions)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return "Error converting and autorotating", err
	}
	origDims, err := img.Size()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return "Error getting image size", err
	}
	origPath := path.Join(save_to, "orig.jpeg")
	saveImageLocal(origImage, origPath)
	for screenSize, dim := range dims {
		for _, iType := range imageTypes {
			savePath := buildPath(save_to, screenSize, iType)
			newDims := smartDims(origDims, dim)
			newImage, err := bimg.NewImage(origImage).Resize(newDims.Width, newDims.Height)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return fmt.Sprintf("Error resizing to %dWx%dH\n", newDims.Width, newDims.Height), err
			}
			newImage, err = bimg.NewImage(newImage).Convert(iType)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return fmt.Sprintf("Error converting to %s\n", bimg.ImageTypeName(iType)), err
			}
			saveImageLocal(newImage, savePath)
		}
	}
	thumbnail, err := bimg.NewImage(origImage).Thumbnail(thumbSize)
	saveImageLocal(thumbnail, path.Join(save_to, "thumbnail.jpeg"))
	if err != nil {
		return fmt.Sprintf("Error creating thumbnail: %v", err.Error()), err
	}
	return "Success", nil
}

func downloadImage(bucket string, key string, client *s3.Client, ctx context.Context) (*bimg.Image, error) {
	// Download image from s3 bucket and return it to be processed

	params := &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}
	response, err := client.GetObject(ctx, params)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return nil, err
	}

	defer response.Body.Close()

	buffer, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	img := bimg.NewImage(buffer)
	if !bimg.IsTypeNameSupported(img.Type()) {
		return nil, errors.New("invalid image type")
	}
	fmt.Printf("Downloaded %s", key)
	return img, nil
}

func splitKey(s3ObjectKey string) (string, string, error) {
	// separate filename from path in s3ObjectKey
	// e.g. media/images/tags/bread/orig.jpg -> "media/images/tags/bread", "orig.jpg"
	lastSlash := strings.LastIndex(s3ObjectKey, "/")
	if lastSlash == len(s3ObjectKey)-1 {
		return "", "", errors.New("no filename found in S3 Object Key")
	}
	prefix := s3ObjectKey[0:lastSlash]
	filename := s3ObjectKey[lastSlash+1:]
	return prefix, filename, nil
}

func Handler(ctx context.Context, event events.S3Event) (string, error) {

	// get bucket and item from event,
	source_bucket := event.Records[0].S3.Bucket.Name
	source_object := event.Records[0].S3.Object.Key

	// get upload destination. Keep same prefix.
	// e.g. if file came from <source_bucket>/media/images/tags/bread/orig.jpg
	//      upload output files to <destination_bucket>/media/images/tags/bread/1200.webp ... etc.
	destination_bucket := os.Getenv("DESTINATION_BUCKET")
	prefix, filename, err := splitKey(source_object)
	if err != nil {
		fmt.Printf("Error splitting object key: %v", err.Error())
		return "Error", err
	}
	fmt.Printf("Got prefix: %s and Filename: %s\n", prefix, filename)
	output_dir := "/tmp/output"

	// get dimensions
	dimStr := os.Getenv("DIMENSIONS")
	dims, err := parseDims(dimStr)
	if err != nil {
		fmt.Printf("Error in Dimensions:  %v\n", err.Error())
		return "Error", err
	}

	// get output formats
	formatStr := os.Getenv("FORMATS")
	formats, err := parseImageTypes(formatStr)

	if err != nil {
		fmt.Printf("Error in output formats: %v\n", err.Error())
	}

	// get thumbnail size
	thumbSizeStr := os.Getenv("THUMB_SIZE")
	thumbSize, err := strconv.Atoi(thumbSizeStr)
	if err != nil {
		thumbSize = 128
		fmt.Printf("Invalid value for THUMB_SIZE: %s\n", thumbSizeStr)
		fmt.Printf("Using default value: 128\n")
	}

	// configure aws
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		fmt.Printf("Configuration Error: %v\n", err.Error())
		return "Error", err
	}
	client := s3.NewFromConfig(cfg)
	// download original image
	original, err := downloadImage(source_bucket, source_object, client, ctx)
	if err != nil {
		fmt.Printf("Error downloading image from s3: %v\n", err.Error())
		return "Error", err
	}

	err = os.MkdirAll(output_dir, 0700)
	if err != nil {
		fmt.Printf("Error creating output directories: %v\n", err.Error())
		return "Error", err
	}

	processImage(original, formats, dims, output_dir, thumbSize)
	files, err := os.ReadDir(output_dir)
	if err != nil {
		fmt.Printf("Error reading %s: %v\n", output_dir, err.Error())
		return "Error", err
	}
	for _, f := range files {
		full_path := filepath.Join(output_dir, f.Name())
		file, err := os.Open(full_path)
		if err != nil {
			fmt.Printf("Error opening output file %s: %v\n", full_path, err.Error())
			return "Error", err
		}
		input := &s3.PutObjectInput{
			Bucket: &destination_bucket,
			Key:    aws.String(fmt.Sprintf("%s/%s", prefix, f.Name())),
			Body:   file,
		}
		_, err = client.PutObject(ctx, input)
		if err != nil {
			fmt.Printf("Error uploading %s: %v\n", full_path, err.Error())
			return "Error", err
		}
	}

	return "Success", nil
}

func main() {
	runLocal := flag.Bool("local", false, "Run locally")
	input := flag.String("input", "", "Absolute path to input file")
	outputDir := flag.String("output", "", "Absolute path to output directory")
	formats := flag.String("formats", "", "Comma separated list of output formats: e.g. \"jpeg,webp,png\" - default \"jpeg,webp\"")
	dimStr := flag.String("dims", "", "List of output dimensions formatted as name1:width1,height1;name2:width2,height2")
	thumbSize := flag.Int("thumbSize", 128, "Size of thumbnail - default 128px")
	flag.Parse()
	if *runLocal {
		img, err := loadImageLocal(*input)
		if err != nil {
			log.Fatalf("Error loading file: %s", *input)
		}
		var dims map[string]bimg.ImageSize

		if *dimStr == "" {
			dims = getDefaultDims()
		} else {
			dims, err = parseDims(*dimStr)
			if err != nil {
				log.Fatalf("Error reading dim string: %v", err.Error())
			}
		}
		iTypes, err := parseImageTypes(*formats)
		if err != nil {
			log.Fatalf("Error reading image formats: %v", err.Error())
		}
		err = os.MkdirAll(*outputDir, 0755)
		if err != nil {
			log.Fatalf("Error creating output directory: %s: %v", *outputDir, err.Error())
		}
		msg, err := processImage(img, iTypes, dims, *outputDir, *thumbSize)
		fmt.Println(msg)
		if err != nil {
			log.Fatalf("Error processing image: %v", err)
		}
	} else {
		lambda.Start(Handler)
	}

}
