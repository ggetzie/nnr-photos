package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const defaultThumbSize = 128

// cacheControl is safe to set aggressively: a derivative's key is determined by
// the folder it lives in, and a re-upload always overwrites the same keys.
const cacheControl = "public, max-age=31536000, immutable"

// s3API is the subset of the S3 client this program uses, so tests can fake it.
type s3API interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// splitKey separates the directory from the filename in an S3 object key, e.g.
// "media/images/tags/bread/orig.jpg" -> "media/images/tags/bread", "orig.jpg".
//
// The directory is the identity of the image set; the filename is discarded.
func splitKey(s3ObjectKey string) (string, string, error) {
	if s3ObjectKey == "" {
		return "", "", errors.New("empty S3 object key")
	}
	if strings.HasSuffix(s3ObjectKey, "/") {
		return "", "", errors.New("no filename found in S3 Object Key")
	}
	lastSlash := strings.LastIndex(s3ObjectKey, "/")
	if lastSlash < 0 {
		// A key at the bucket root has no prefix. The previous implementation
		// sliced [0:-1] here and panicked.
		return "", s3ObjectKey, nil
	}
	return s3ObjectKey[:lastSlash], s3ObjectKey[lastSlash+1:], nil
}

// downloadImage fetches an object and returns its raw bytes.
func downloadImage(ctx context.Context, client s3API, bucket, key string) ([]byte, error) {
	response, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("getting s3://%s/%s: %w", bucket, key, err)
	}
	defer response.Body.Close()

	buffer, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("reading s3://%s/%s: %w", bucket, key, err)
	}
	return buffer, nil
}

// uploadDerivatives writes every derivative under the given key prefix.
func uploadDerivatives(ctx context.Context, client s3API, bucket, prefix string, derivatives []Derivative) error {
	for _, d := range derivatives {
		key := d.Filename()
		if prefix != "" {
			key = prefix + "/" + key
		}
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:       aws.String(bucket),
			Key:          aws.String(key),
			Body:         bytes.NewReader(d.Data),
			ContentType:  aws.String(d.ContentType()),
			CacheControl: aws.String(cacheControl),
		})
		if err != nil {
			return fmt.Errorf("uploading s3://%s/%s: %w", bucket, key, err)
		}
	}
	return nil
}

// settings holds the run configuration read from the environment.
type settings struct {
	destinationBucket string
	dims              map[string]ImageSize
	formats           []ImageFormat
	thumbSize         int
}

// loadSettings reads configuration from the environment. Empty and unset are
// indistinguishable via os.Getenv, so both mean "use the default".
func loadSettings() (settings, error) {
	var s settings

	s.destinationBucket = os.Getenv("DESTINATION_BUCKET")
	if s.destinationBucket == "" {
		return s, errors.New("DESTINATION_BUCKET is not set")
	}

	var err error
	if s.dims, err = parseDims(os.Getenv("DIMENSIONS")); err != nil {
		return s, fmt.Errorf("DIMENSIONS: %w", err)
	}
	// Previously this error was logged but not returned, leaving formats nil --
	// a typo in FORMATS silently produced only orig.jpeg and thumbnail.jpeg
	// and still reported success.
	if s.formats, err = parseImageTypes(os.Getenv("FORMATS")); err != nil {
		return s, fmt.Errorf("FORMATS: %w", err)
	}

	s.thumbSize = defaultThumbSize
	if raw := os.Getenv("THUMB_SIZE"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return s, fmt.Errorf("THUMB_SIZE: invalid value %q", raw)
		}
		s.thumbSize = n
	}
	return s, nil
}

// Handler processes an S3 ObjectCreated event.
func Handler(ctx context.Context, event events.S3Event) (string, error) {
	if len(event.Records) == 0 {
		return "Error", errors.New("event contained no records")
	}

	cfg, err := loadSettings()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		return "Error", err
	}

	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "AWS configuration error: %v\n", err)
		return "Error", err
	}
	client := s3.NewFromConfig(awsCfg)

	for _, record := range event.Records {
		if err := handleRecord(ctx, client, cfg, record); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return "Error", err
		}
	}
	return "Success", nil
}

func handleRecord(ctx context.Context, client s3API, cfg settings, record events.S3EventRecord) error {
	sourceBucket := record.S3.Bucket.Name

	// S3 URL-encodes object keys in event notifications (a space arrives as
	// "+"). Without decoding, GetObject 404s on any key containing one.
	sourceObject, err := url.QueryUnescape(record.S3.Object.Key)
	if err != nil {
		return fmt.Errorf("decoding object key %q: %w", record.S3.Object.Key, err)
	}

	prefix, filename, err := splitKey(sourceObject)
	if err != nil {
		return fmt.Errorf("splitting object key %q: %w", sourceObject, err)
	}
	fmt.Printf("Processing s3://%s/%s (prefix %q, filename %q)\n", sourceBucket, sourceObject, prefix, filename)

	data, err := downloadImage(ctx, client, sourceBucket, sourceObject)
	if err != nil {
		return err
	}

	img, format, err := decodeImage(data)
	if err != nil {
		return fmt.Errorf("decoding %s: %w", sourceObject, err)
	}
	fmt.Printf("Decoded %s as %s (%dx%d)\n", sourceObject, format, img.Bounds().Dx(), img.Bounds().Dy())

	derivatives, err := processImage(img, cfg.formats, cfg.dims, cfg.thumbSize)
	if err != nil {
		return fmt.Errorf("processing %s: %w", sourceObject, err)
	}

	if err := uploadDerivatives(ctx, client, cfg.destinationBucket, prefix, derivatives); err != nil {
		return err
	}
	fmt.Printf("Uploaded %d derivatives to s3://%s/%s\n", len(derivatives), cfg.destinationBucket, prefix)
	return nil
}
