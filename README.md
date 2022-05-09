# No Nonsense Recipes Photo Optimizer

This is an AWS lambda function to automatically optimize photos uploaded to [No Nonsense Recipes](https://nononsense.recipes)

It's written in [Go](https://go.dev/) and uses the [bimg](https://pkg.go.dev/github.com/h2non/bimg) package.

bimg depends on [libvips](https://www.libvips.org/), so a Docker image modifying the [AWS Lambda base image](https://github.com/aws/aws-lambda-base-images/blob/go1.x/Dockerfile.go1.x) to install libvips is included and all other necessary libraries to support JPEG, WEBP, PNG, GIF, HEIF, and TIFF formats.

## Lambda Usage

Replace `1234567890` below with the appropriate address for your AWS ECR repository.

- Retrieve authentication token and authenticate docker to your ECR repository
  `aws ecr get-login-password --region us-east-1 | docker login --username AWS --password-stdin 1234567890.dkr.ecr.us-east-1.amazonaws.com`
- Build Docker image  
  `docker build -t nnr-photos`
- After the build completes tag the latest image  
  `docker tag nnr-photos:latest 1234567890.dkr.ecr.us-east-1.amazonaws.com/nnr-photos:latest`
- Push to the AWS repository  
  `docker push 1234567890.dkr.ecr.us-east-1.amazonaws.com/nnr-photos:latest`
  

## Command Line usage

This program can also be run locally from the command line with the `--local` option. Make sure you have all the necessary libraries installed for libvips and all the file formats you wish to use. Note that for some libraries libvips might require a newer version than is available in your distribution's package repository, so it may be necessary to compile from source. See the [Dockerfile](./Dockerfile) for how to install all the libraries necessary to support PNG, GIF, TIFF, HEIF, JPEG, and WEBP files.

When all libraries are installed, simply build and place the binary in your `$PATH`

```bash
go build -o /path/to/photos photos.go
sudo ln -s /path/to/photos /usr/local/bin/photos
```

Specify the input file, output directory, desired output file types, desired dimensions, and thumbnail size.

```bash
photos --local --input=/home/gabe/images_raw/input.png \
--output=/home/gabe/images_processed/ \
--dims="web-size:300,400;mobile-size:150,200" \
--formats="jpeg,webp" \
--thumbSize=64
```

It's also easy to process an entire directory of images at a time with a small script.

```bash
#!/usr/bin/env bash

function convertImage () {
  OUTPUT_DIR="/home/gabe/images_processed"
  # get filename from absolute 
  filename=`echo $1 | cut -d'/' -f8` path

  # remove extension from filename to use as directory name
  imgdir=`echo $filename | sed -E 's/\.[^.]+$//'` 

  photos --local --input="$1" --output="${OUTPUT_DIR}/${imgdir}" \
  --formats="jpeg,webp" --dims="opt:300,500;mob:150,400" --thumbSize=64
}

export -f convertImage

find /home/gabe/images_raw -type f -print0 | \
xargs -0 -P8 -I  {} bash -c 'convertImage "{}"' _ {}
```
