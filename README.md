# No Nonsense Recipes Photo Optimizer: nnr-photos

This program was created to automatically optimize photos uploaded to [No Nonsense Recipes](https://nononsense.recipes). It is intended to be run as an AWS Lambda function triggered on an S3 `ObjectCreated` event. It can also be run as a command line program (see [Command Line Usage](#command-line-usage) below).

It's written in [Go](https://go.dev/) and uses the [bimg](https://pkg.go.dev/github.com/h2non/bimg) package.

bimg depends on [libvips](https://www.libvips.org/), so a Docker image modifying the [AWS Lambda base image](https://github.com/aws/aws-lambda-base-images/blob/go1.x/Dockerfile.go1.x) to install libvips is included with all necessary libraries to support JPEG, WEBP, PNG, GIF, HEIF, and TIFF formats.

`nnr-photos` performs a number of common operations to optimize images for the web:

- strips EXIF data (removes any identifying information that may be present such as camera type, geolocation, etc.)
- Auto-Rotate - aligns image orientation to match EXIF orientation
- Convert to jpeg - converts all input files to JPEG with the original dimensions
- Create thumbnails
- Resize to common screen-friendly dimensions and convert to common formats. By default, `nnr-photos` will output jpeg and webp formats in the following dimensions:    

### Default Output Dimensions
|Name  |Width|Height|Screen width|
|---   |---  |---   |---         |
|"1200"|1090 |818   | >= 1200px  |
|"992" |910  |683   | >= 992px   |
|"768" |670  |503   | >= 768px   |
|"576" |515  |386   | >= 576px   |
|"408" |400  |300   | >= 408px   |
|"320" |310  |225   | >= 320px   |

  
### Example Output

```
images_raw/          ->               images_processed/
└── bread.png                         └── bread
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

The output files can then be used with `<picture>` tag to display the best size photo for each user depending on their screen size.

```html
<picture>
  <source media="(min-width:1200px)" 
          srcset="/media/images/tags/bread/1200.webp">
  <source media="(min-width:1200px)" 
          srcset="/media/images/tags/bread/1200.jpeg">
  <source media="(min-width:992px)" 
          srcset="/media/images/tags/bread/992.webp">
  <source media="(min-width:992px)" 
          srcset="/media/images/tags/bread/992.jpeg">
  <source media="(min-width:768px)" 
          srcset="/media/images/tags/bread/768.webp">
  <source media="(min-width:768px)" 
          srcset="/media/images/tags/bread/768.jpeg">
  <source media="(min-width:576px)" 
          srcset="/media/images/tags/bread/576.webp">
  <source media="(min-width:576px)" 
          srcset="/media/images/tags/bread/576.jpeg">
  <source media="(min-width:408px)" 
          srcset="/media/images/tags/bread/408.webp">
  <source media="(min-width:408px)" 
          srcset="/media/images/tags/bread/408.jpeg">
  <source media="(min-width:320px)" 
          srcset="/media/images/tags/bread/320.webp">
  <source media="(min-width:320px)" 
          srcset="/media/images/tags/bread/320.jpeg">
  <img src="/media/images/tags/bread/orig.jpeg">
</picture>
```

Output formats and dimensions can be customized by setting the `DIMENSIONS`, `FORMATS`, `THUMB_SIZE` environment variables when used in a Lambda function or the `--dims`, `--formats`, `--thumbSize` flags when used at the command line. 

- `DIMENSIONS` or `--dims` accepts a string in the format "name1:width1,height1;name2:width2,height2" e.g. "web-size:800,600;mobile-size:400,300"
- `FORMATS` or `--formats` accepts a string of comma-separated image format extensions e.g. "jpeg,png,webp"
- `THUMB_SIZE` of `--thumbSize` accepts a single integer which will be the height and width, in pixels of the thumbnail.

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

  To use create a Lambda function and deploy with a container image: [AWS Docs](https://docs.aws.amazon.com/lambda/latest/dg/go-image.html)

  Set an environment variable `DESTINATION_BUCKET` to the name of the S3 bucket where you would like the processed images to be saved.

  Create an S3 `ObjectCreated` event trigger for the function so it runs every time a new images is uploaded to the source bucket. Note AWS recommends using separate buckets to avoid an infinite loop of recursive lambda calls.

  Optionally set environment variables for `DIMENSIONS`, `FORMATS`, and `THUMB_SIZE` to use custom values instead of the defaults.
  
  

## Command Line Usage

This program can also be run locally from the command line with the `--local` option. Make sure you have all the necessary libraries installed for libvips and all the file formats you wish to use. Note that for some libraries libvips might require a newer version than is available in your distribution's package repository, so it may be necessary to compile them from source. See the [Dockerfile](./Dockerfile) for how to install all the libraries necessary to support PNG, GIF, TIFF, HEIF, JPEG, and WEBP files.

When all libraries are installed, simply build and place the binary in your `$PATH`

```bash
go build -o /path/to/photos photos.go
sudo ln -s /path/to/photos /usr/local/bin/photos
```

Specify the input file, output directory, desired output file types, desired dimensions, and thumbnail size.

```bash
photos --local --input=/path/to/images_raw/input.png \
--output=/path/to/images_processed/ \
--dims="web-size:300,400;mobile-size:150,200" \
--formats="jpeg,webp" \
--thumbSize=64
```

It's also easy to process an entire directory of images at a time with a small script. The script below will process all images in a directory called `images_raw` and place the output in subfolders in the directory `images_processed`.

```bash
#!/usr/bin/env bash

function convertImage () {
  OUTPUT_DIR="/path/to/images_processed"

  # get filename without extension to use as subdirectory name
  imgdir=`echo $1 | awk -F / '{print $(NF)}' | awk -F . '{print $1}'`

  photos --local --input="$1" --output="${OUTPUT_DIR}/${imgdir}" \
  --formats="jpeg,webp" \
  --dims="web-size:300,500;mobile-size:150,400" \
  --thumbSize=64
}

export -f convertImage

find /path/to/images_raw -type f -print0 | \
xargs -0 -P8 -I  {} bash -c 'convertImage "{}"' _ {}
```
