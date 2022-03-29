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
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-lambda-go/lambdacontext"
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
	// adjust dimensions to match width, perserving aspect ratio
	newHeight := originalDims.Height * width / originalDims.Width
	return bimg.ImageSize{Width: width, Height: newHeight}
}

func smartDims(originalDims bimg.ImageSize, maxDims bimg.ImageSize) bimg.ImageSize {
	// Calculate dims within max width and height, perserving aspect ratio
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
	save_to string) (string, error) {
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
				return fmt.Sprintf("Error resizing to %dWx%dH", newDims.Width, newDims.Height), err
			}
			newImage, err = bimg.NewImage(newImage).Convert(iType)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return fmt.Sprintf("Error converting to %s", bimg.ImageTypeName(iType)), err
			}
			saveImageLocal(newImage, savePath)
		}
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
	return img, nil
}

func splitKey(s3ObjectKey string) (string, string, error) {
	lastSlash := strings.LastIndex(s3ObjectKey, "/")
	if lastSlash == len(s3ObjectKey)-1 {
		return "", "", errors.New("No filename found in S3 Object Key!")
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
	prefix, _, err := splitKey(source_object)
	if err != nil {
		log.Fatalf("Error splitting object key: %v", err.Error())
	}
	fmt.Printf("Got prefix: %s\n", prefix)
	output_dir := "/tmp/output"

	// configure aws
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Fatalf("Configuration Error: %v", err.Error())
	}
	client := s3.NewFromConfig(cfg)
	// download original image
	original, err := downloadImage(source_bucket, source_object, client, ctx)
	if err != nil {
		log.Fatalf("Error downloading image from s3: %v", err.Error())
	}

	os.MkdirAll(output_dir, 0700)
	processImage(original, getDefaultImageTypes(), getDefaultDims(), output_dir)
	files, err := os.ReadDir(output_dir)
	if err != nil {
		log.Fatalf("Error reading %s: %v", output_dir, err.Error())
	}
	for _, f := range files {
		full_path := filepath.Join(output_dir, f.Name())
		file, err := os.Open(full_path)
		if err != nil {
			log.Fatalf("Error opening output file %s: %v", full_path, err.Error())
		}
		input := &s3.PutObjectInput{
			Bucket: &destination_bucket,
			Key:    aws.String(fmt.Sprintf("%s/%s", prefix, f.Name())),
			Body:   file,
		}
		_, err = client.PutObject(ctx, input)
		if err != nil {
			log.Fatalf("Error uploading %s: %v", full_path, err.Error())
		}
	}

	return "Success", nil
}

func testHandler(ctx context.Context, event events.S3Event) (string, error) {
	lc, _ := lambdacontext.FromContext(ctx)
	fmt.Println(fmt.Sprintf("Lambda Context %v", lc))
	key := event.Records[0].S3.Object.Key
	prefix, filename, err := splitKey(key)
	if err != nil {
		log.Fatalf("Error splitting object key: %v", err.Error())
	}
	fmt.Println(fmt.Sprintf("Bucket: %s", event.Records[0].S3.Bucket.Name))
	fmt.Println(fmt.Sprintf("Object: %s", key))
	fmt.Println(fmt.Sprintf("Prefix: %s, Filename: %s", prefix, filename))
	return "success", nil
}

func main() {
	runLocal := flag.Bool("local", false, "Run locally")
	input := flag.String("input", "", "Absolute path to input file")
	outputDir := flag.String("output", "", "Absolute path to output directory")
	flag.Parse()
	if *runLocal {
		img, err := loadImageLocal(*input)
		if err != nil {
			log.Fatalf("Error loading file: %s", *input)
		}
		msg, err := processImage(img, getDefaultImageTypes(), getDefaultDims(), *outputDir)
		fmt.Println(msg)
		if err != nil {
			log.Fatalf("Error processing image: %v", err)
		}
	} else {
		lambda.Start(Handler)
	}

}
