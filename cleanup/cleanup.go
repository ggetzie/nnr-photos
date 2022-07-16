package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
		return "", errors.New("No filename found in S3 Object Key!")
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
		return "Error", errors.New("DESTINATION_BUCKET environment variable not set!")
	}

	client := s3.NewFromConfig(cfg)
	sourceObject := event.Records[0].S3.Object.Key
	prefix, err := getDestinationPrefix(sourceObject)
	if err != nil {
		return "Error", err
	}
	params := s3.DeleteObjectInput{
		Bucket: &destinationBucket,
		Key:    aws.String(prefix),
	}
	res, err := client.DeleteObject(ctx, &params)

	if err != nil {
		fmt.Println(fmt.Sprintf("Error deleting %s from %s", prefix, destinationBucket))
		return "Error", err
	}

	fmt.Println(fmt.Sprintf("Delete Output %v", res))

	return "Success", nil
}

func main() {
	lambda.Start(Handler)
}
