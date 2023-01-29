package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
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
	lastSlash := strings.LastIndex(s3ObjectKey, "/")
	if lastSlash == len(s3ObjectKey)-1 {
		return "Error - getDestinationPrefix", errors.New("no filename found in S3 Object Key")
	}
	prefix := s3ObjectKey[0:lastSlash]
	return prefix, nil
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
	sourceObject := event.Records[0].S3.Object.Key
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

	deleteParams := s3.DeleteObjectsInput{
		Bucket: aws.String(destinationBucket),
		Delete: &types.Delete{Objects: toDelete},
	}
	res, err := client.DeleteObjects(ctx, &deleteParams)

	if err != nil {
		fmt.Printf("Error deleting %s from %s\n", prefix, destinationBucket)
		return "Error", err
	}

	fmt.Printf("Delete Output %v\n", res)

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
