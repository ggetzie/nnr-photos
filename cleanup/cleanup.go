package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func getDestinationPrefix(s3ObjectKey string) (string, error) {
	// objects deleted in source bucket will be a single file
	// e.g. media/images/tags/bread/orig.jpeg
	// this will correspond to a folder of images to delete in the destination bucket
	// e.g. media/images/tags/bread/1200.webp
	//      media/images/tags/bread/1200.jpeg
	//      media/images/tags/bread/920.webp etc.
	// we want to delete all media/images/tags/bread/* in the destination bucket
	if s3ObjectKey == "" {
		return "", errors.New("empty S3 object key")
	}
	if strings.HasSuffix(s3ObjectKey, "/") {
		return "", errors.New("no filename found in S3 Object Key")
	}
	lastSlash := strings.LastIndex(s3ObjectKey, "/")
	if lastSlash < 0 {
		// Previously this sliced [0:-1] and panicked. Returning an empty
		// prefix would be far worse here than a panic: this is a delete path,
		// and an empty prefix matches every object in the bucket.
		return "", fmt.Errorf("refusing to derive a prefix from key %q: no directory component", s3ObjectKey)
	}
	return s3ObjectKey[:lastSlash], nil
}

func Handler(ctx context.Context, event events.S3Event) (string, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		return "Error", err
	}

	destinationBucket := os.Getenv("DESTINATION_BUCKET")
	if destinationBucket == "" {
		return "Error", errors.New("environment variable DESTINATION_BUCKET not set")
	}

	maxKeys, err := strconv.Atoi(os.Getenv("MAX_KEYS"))

	if err != nil {
		return "Error - MAX_KEYS", err
	}

	client := s3.NewFromConfig(cfg)
	if len(event.Records) == 0 {
		return "Error", errors.New("event contained no records")
	}
	// S3 URL-encodes object keys in event notifications.
	sourceObject, err := url.QueryUnescape(event.Records[0].S3.Object.Key)
	if err != nil {
		return "Error", fmt.Errorf("decoding object key %q: %w", event.Records[0].S3.Object.Key, err)
	}
	prefix, err := getDestinationPrefix(sourceObject)
	if err != nil {
		return "Error", err
	}
	listParams := s3.ListObjectsV2Input{
		Bucket:  aws.String(destinationBucket),
		Prefix:  aws.String(prefix),
		MaxKeys: int32(maxKeys),
	}
	var toDelete []types.ObjectIdentifier

	listOutput, err := client.ListObjectsV2(context.TODO(), &listParams)

	if err != nil {
		return "Error", err
	}

	for _, object := range listOutput.Contents {
		toDelete = append(toDelete, types.ObjectIdentifier{Key: aws.String(*object.Key)})
	}

	if len(toDelete) == 0 {
		fmt.Printf("Nothing to delete under %s/%s\n", destinationBucket, prefix)
		return "Success", nil
	}

	deleteParams := s3.DeleteObjectsInput{
		Bucket: aws.String(destinationBucket),
		Delete: &types.Delete{Objects: toDelete},
	}
	res, err := client.DeleteObjects(ctx, &deleteParams)
	if err != nil {
		return "Error", fmt.Errorf("deleting %d objects under %s/%s: %w",
			len(toDelete), destinationBucket, prefix, err)
	}

	// DeleteObjects reports per-object failures in res.Errors rather than
	// returning an error, so without this check a partial failure is
	// indistinguishable from a clean run.
	if len(res.Errors) > 0 {
		for _, e := range res.Errors {
			fmt.Printf("Failed to delete %s: %s %s\n",
				aws.ToString(e.Key), aws.ToString(e.Code), aws.ToString(e.Message))
		}
		return "Error", fmt.Errorf("deleted %d of %d objects under %s/%s, %d failed",
			len(res.Deleted), len(toDelete), destinationBucket, prefix, len(res.Errors))
	}

	fmt.Printf("Deleted %d objects under %s/%s\n", len(res.Deleted), destinationBucket, prefix)

	// This function reads a single ListObjectsV2 page and does not paginate,
	// so a truncated listing means the folder is only partly cleaned. Test
	// IsTruncated rather than comparing counts: the standard derivative set is
	// exactly MAX_KEYS objects, so a count comparison warns on every clean run.
	if listOutput.IsTruncated {
		fmt.Printf("WARNING: more than MAX_KEYS (%d) objects under %s/%s; some were not deleted\n",
			maxKeys, destinationBucket, prefix)
	}

	return "Success", nil
}

func main() {
	runLocal := flag.Bool("local", false, "Run locally")
	source := flag.String("source", "nnr-media-raw", "source bucket")
	dest := flag.String("dest", "nnr-static", "destination bucket")
	prefix := flag.String("prefix", "media/images/tags/bread", "prefix for objects to delete")
	flag.Parse()
	if *runLocal {
		fmt.Printf("source=%s, dest=%s, prefix=%s\n", *source, *dest, *prefix)
		cfg, err := config.LoadDefaultConfig(context.TODO())
		if err != nil {
			log.Fatal(err)
		}
		client := s3.NewFromConfig(cfg)
		listParams := s3.ListObjectsV2Input{
			Bucket:  aws.String(*dest),
			MaxKeys: 14,
			Prefix:  prefix,
		}
		output, err := client.ListObjectsV2(context.TODO(), &listParams)
		if err != nil {
			log.Fatal(err)
		}
		var toDelete []types.ObjectIdentifier
		fmt.Println("DELETING:")
		for _, object := range output.Contents {
			fmt.Printf("key=%s, size=%d\n", aws.ToString(object.Key), object.Size)
			toDelete = append(toDelete, types.ObjectIdentifier{Key: aws.String(*object.Key)})
		}
		_, err = client.DeleteObjects(context.TODO(), &s3.DeleteObjectsInput{
			Bucket: aws.String(*dest),
			Delete: &types.Delete{Objects: toDelete},
		})
		if err != nil {
			log.Fatal(err)
		}

	} else {
		lambda.Start(Handler)
	}
}
