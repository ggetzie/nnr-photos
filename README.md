# No Nonsense Recipes Photo Optimizer: nnr-photos

This program was created to automatically optimize photos uploaded to [No Nonsense Recipes](https://nononsense.recipes). It is intended to be run as an AWS Lambda function triggered on an S3 `ObjectCreated` event. It can also be run as a command line program (see [Command Line Usage](#command-line-usage) below).

It's written in [Go](https://go.dev/) and uses the [bimg](https://pkg.go.dev/github.com/h2non/bimg) package.

bimg depends on [libvips](https://www.libvips.org/), so a Docker image modifying the [AWS Lambda base image](https://github.com/aws/aws-lambda-base-images/blob/go1.x/Dockerfile.go1.x) to install libvips is included and all other necessary libraries to support JPEG, WEBP, PNG, GIF, HEIF, and TIFF formats.

`nnr-photos` performs a number of common operations to optimize images for the web:

- strips EXIF data (removes any identifying information that may be present such as camera type, geolocation, etc.)
- Auto-Rotate - aligns image orientation to match EXIF orientation
- Convert to jpeg - converts all input files to JPEG with the original dimensions
- Create thumbnails
- Resize to common screen-friendly dimensions and convert to common formats. By default, `nnr-photos` will output jpeg and webp formats in the following dimensions:    
  |Name  |Width|Height|Screen width|
  |---   |---  |---   |---         |
  |"1200"|1090 | 818  | >= 1200px  |
	|"992" |910  |683   | >= 992px   |
	|"768" |670  | 503  | >= 768px   |
	|"576" | 515 | 386  | >= 576px   |
	|"408" | 400 | 300  | >= 408px   |
	|"320" | 310 | 225  | >= 320px   |


### Example Output

```
input                ->               images_processed/
└── somePic.png                       └── somePic
                                          ├── 1200.jpeg
                                          ├── 1200.webp
                                          ├── 320.jpeg
                                          ├── 320.webp
                                          ├── 408.jpeg
                                          ├── 408.webp
                                          ├── 576.jpeg
                                          ├── 576.webp
                                          ├── 768.jpeg
                                          ├── 768.webp
                                          ├── 992.jpeg
                                          ├── 992.webp
                                          ├── orig.jpeg
                                          └── thumbnail.jpeg

```

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
  

## Command Line Usage

This program can also be run locally from the command line with the `--local` option. Make sure you have all the necessary libraries installed for libvips and all the file formats you wish to use. Note that for some libraries libvips might require a newer version than is available in your distribution's package repository, so it may be necessary to compile them from source. See the [Dockerfile](./Dockerfile) for how to install all the libraries necessary to support PNG, GIF, TIFF, HEIF, JPEG, and WEBP files.

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
