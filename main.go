package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
)

func main() {
	runLocal := flag.Bool("local", false, "Run locally")
	input := flag.String("input", "", "Absolute path to input file")
	outputDir := flag.String("output", "", "Absolute path to output directory")
	formats := flag.String("formats", "", "Comma separated list of output formats, e.g. \"jpeg,webp,png\" - default \"jpeg,webp\"")
	dimStr := flag.String("dims", "", "List of output dimensions formatted as name1:width1,height1;name2:width2,height2")
	thumbSize := flag.Int("thumbSize", defaultThumbSize, "Size of thumbnail in px - default 128")
	flag.Parse()

	if !*runLocal {
		lambda.Start(Handler)
		return
	}

	if *input == "" || *outputDir == "" {
		flag.Usage()
		log.Fatal("--input and --output are required with --local")
	}
	if err := runLocalMode(*input, *outputDir, *formats, *dimStr, *thumbSize); err != nil {
		log.Fatal(err)
	}
}

func runLocalMode(input, outputDir, formats, dimStr string, thumbSize int) error {
	data, err := os.ReadFile(input)
	if err != nil {
		return fmt.Errorf("reading %s: %w", input, err)
	}

	dims, err := parseDims(dimStr)
	if err != nil {
		return fmt.Errorf("reading dims: %w", err)
	}
	iTypes, err := parseImageTypes(formats)
	if err != nil {
		return fmt.Errorf("reading formats: %w", err)
	}

	start := time.Now()
	img, format, err := decodeImage(data)
	if err != nil {
		return fmt.Errorf("decoding %s: %w", input, err)
	}
	fmt.Printf("Decoded %s as %s (%dx%d) in %v\n",
		filepath.Base(input), format, img.Bounds().Dx(), img.Bounds().Dy(),
		time.Since(start).Round(time.Millisecond))

	derivatives, err := processImage(img, iTypes, dims, thumbSize)
	if err != nil {
		return fmt.Errorf("processing image: %w", err)
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating output directory %s: %w", outputDir, err)
	}
	for _, d := range derivatives {
		path := filepath.Join(outputDir, d.Filename())
		if err := os.WriteFile(path, d.Data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}

	fmt.Printf("Wrote %d files to %s in %v\n",
		len(derivatives), outputDir, time.Since(start).Round(time.Millisecond))
	return nil
}
