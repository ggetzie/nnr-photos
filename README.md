# No Nonsense Recipes Photo Optimizer: nnr-photos

This program was created to automatically optimize photos uploaded to [No Nonsense Recipes](https://nononsense.recipes). It is intended to be run as an AWS Lambda function triggered on an S3 `ObjectCreated` event. It can also be run as a command line program (see [Command Line Usage](#command-line-usage) below).

It's written in [Go](https://go.dev/) with **no cgo and no native dependencies**. Everything is the standard library plus [`golang.org/x/image`](https://pkg.go.dev/golang.org/x/image), [`gen2brain/webp`](https://github.com/gen2brain/webp) for WebP encoding and [`gen2brain/heic`](https://github.com/gen2brain/heic) for HEIC decoding, both of which are CGo-free.

That means it builds with `CGO_ENABLED=0` and ships as a **5.5 MB zip** (14 MB unzipped) on the `provided.al2023` runtime. There is no container image, no ECR repository, and nothing to install on a development machine beyond Go itself.

> This program previously used [bimg](https://pkg.go.dev/github.com/h2non/bimg)/[libvips](https://www.libvips.org/), which required a Docker image that compiled libvips, libwebp, libheif, libde265 and x265 from source and weighed over 1 GB. See [Input formats](#input-formats) for what changed behaviourally.

### Input formats

| Format | Decoded | Notes |
|---|---|---|
| JPEG | yes | including EXIF orientation |
| PNG | yes | transparency is composited onto **white** (see below) |
| HEIC | yes | including grid/tiled images, which is how phones store them |
| GIF | yes | first frame |
| TIFF | yes | |
| WebP | yes | |

Output is always JPEG and/or WebP (PNG is available via `--formats` but unused by the site).

### Behavioural notes

A few details worth knowing, mostly carried over deliberately from the libvips implementation:

- **`thumbnail.jpeg` is a centre-cropped square at quality 95.** Every other derivative is quality 75. This matches the old `bimg.Thumbnail`, which hardcoded `Crop: true, Quality: 95`.
- **Transparent PNGs are composited onto white.** libvips dropped the alpha band without flattening; Go's JPEG encoder reads alpha-premultiplied values, which would turn transparent regions black. White is the sensible result for a recipe page, so it is done explicitly.
- **Derivatives are chained largest to smallest on raw pixels.** The old code re-decoded `orig.jpeg` for every derivative, stacking a fresh generation of JPEG loss onto each. WebP files are therefore slightly *larger* than before at the same nominal quality, because more real detail survives to the encoder.
- **Nothing is written to `/tmp`.** Derivatives are held in memory and uploaded from there, so a warm container can no longer re-upload a previous invocation's files under a new key prefix.
- **Uploads set `Content-Type` and a long `Cache-Control`.** Previously S3 served every derivative as `binary/octet-stream`.
- Inputs above 40 megapixels are rejected. libvips used to shrink on load; pure Go must decode at full resolution, so the ceiling is explicit.

HEIC inputs pay a further ~575 ms on the *first* decode in a container while the embedded WASM decoder is compiled (~230 ms per decode after that).

Processing a 12 MP photo takes roughly 2 seconds and peaks around 260 MB of RSS - slower per invocation than libvips, but the cold start is far better (a static binary in a zip versus pulling a 1 GB image and dynamically linking against `/usr/local/lib`).

`nnr-photos` performs a number of common operations to optimize images for the web:

- strips EXIF data (removes any identifying information that may be present such as camera type, geolocation, etc.)
- Auto-Rotate - aligns image orientation to match EXIF orientation
- Convert to jpeg - converts all input files to JPEG with the original dimensions
- Create thumbnails - a centre-cropped square at JPEG quality 95
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

Build the deployment zip and push it:

```bash
make lambda                       # -> photos-lambda.zip (~5.5 MB)
make deploy NAME=nnr-photos       # aws lambda update-function-code
```

`make lambda` produces a stripped, statically linked `bootstrap` binary for `arm64`. Build for Intel with `make lambda ARCH=amd64`.

The function must be configured as:

| Setting | Value |
|---|---|
| Package type | Zip |
| Runtime | `provided.al2023` |
| Handler | `bootstrap` |
| Architecture | `arm64` |
| Memory | **1769 MB** (the point where Lambda allocates a full vCPU) |
| Timeout | 60 s |

Set `DESTINATION_BUCKET` to the S3 bucket where processed images should be saved.

Create an S3 `ObjectCreated` event trigger so the function runs whenever a new image is uploaded to the source bucket. The source and destination buckets **must differ**, or the trigger recurses.

Optionally set `DIMENSIONS`, `FORMATS`, and `THUMB_SIZE` to override the defaults.

Test an invocation with the sample event:

```bash
aws lambda invoke --function-name nnr-photos --payload fileb://s3_test.json /dev/stdout
```

### Migrating from the old container image

A Lambda function's `PackageType` is **immutable** - an `Image` function cannot be updated into a `Zip` function. Migration means:

1. Create a *new* function with the settings in the table above, reusing the existing IAM role and environment variables.
2. Point the S3 `ObjectCreated` notification at it, and add the `lambda:InvokeFunction` resource policy for `s3.amazonaws.com`.
3. Verify with `aws lambda invoke`.
4. Remove the old notification, then delete the old function and its ECR repository.

## Command Line Usage

This program can also be run locally from the command line with the `--local` option. There are no system prerequisites - Go is the only requirement.

```bash
make build                        # -> build/photos
sudo ln -s "$PWD/build/photos" /usr/local/bin/photos
```

The Django development server shells out to `build/photos` from `recipes/signals.py`, so keep that path intact.

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
