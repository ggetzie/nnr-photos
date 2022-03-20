# No Nonsense Recipes Photo Optimizer

This is a lambda function to automatically optimize photos uploaded to [No Nonsense Recipes](https://nononsense.recipes)

It's written in [Go](https://go.dev/) and uses the [bimg](https://pkg.go.dev/github.com/h2non/bimg) package for speed.

bimg depends on [libvips](https://www.libvips.org/), so a Docker image modifying the [AWS Lambda base image](https://github.com/aws/aws-lambda-base-images/blob/go1.x/Dockerfile.go1.x) to install libvips is included.
