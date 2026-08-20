# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`nnr-photos` optimizes images for [No Nonsense Recipes](https://nononsense.recipes). It is one Go program with two run modes selected by the `--local` flag in `main()`:

- **no flag** → `lambda.Start(Handler)`, an AWS Lambda triggered by an S3 `ObjectCreated` event
- **`--local`** → CLI that reads one file from disk and writes the output set to a directory

Image work is done by [bimg](https://pkg.go.dev/github.com/h2non/bimg), which is cgo bindings over **libvips**. Nothing here builds or runs without libvips installed.

## Two separate Go modules

The repo contains two independent modules with **no `go.work` and no parent/child relationship**:

| Path | Module | Purpose |
|---|---|---|
| `.` | `github.com/ggetzie/nnr-photos` | the optimizer (`photos.go`) |
| `cleanup/` | `github.com/ggetzie/nnr-photos/cleanup` | separate Lambda; on `ObjectRemoved`, deletes the whole derived-image folder from the destination bucket |

`go build ./...` from the repo root does **not** include `cleanup/`. Run go commands from inside whichever module you are changing. The two modules pin different versions of the AWS SDK and different `go` directives (1.17 vs 1.18) — that is intentional drift, not something to unify unless asked.

`cleanup/` has no Dockerfile; only `photos.go` has a container build.

## Commands

Build/test the optimizer (from repo root):

```bash
go build -o build/photos photos.go
go vet ./...
go test -v .                 # see caveat below
go test -run TestBuildPath .  # single test
```

Build/test the cleanup Lambda:

```bash
cd cleanup && go build . && go vet ./...
```

Local run:

```bash
./build/photos --local --input=/path/in.png --output=/path/outdir \
  --dims="web:800,600;mobile:400,300" --formats="jpeg,webp" --thumbSize=64
```

Container build + ECR push (replace the account id):

```bash
docker build -t nnr-photos .
docker tag nnr-photos:latest 1234567890.dkr.ecr.us-east-1.amazonaws.com/nnr-photos:latest
docker push 1234567890.dkr.ecr.us-east-1.amazonaws.com/nnr-photos:latest
```

`s3_test.json` is a sample S3 `ObjectCreated` event for invoking the Lambda (e.g. `aws lambda invoke --payload fileb://s3_test.json`).

### Test caveat

`photos_test.go` is not hermetic. `TestPrintMetadata`, `TestProcessImage`, and `TestInvalid` hardcode absolute paths on the author's machine (`/media/gabe/data/...`, `/usr/local/src/nnr/nnr/media/images/tags/breakfast/`, the sibling Django project). They fail anywhere else, and `TestProcessImage` writes its output back into the source folder. Only `TestBuildPath` and `TestGetEnv` are portable. Fix a hardcoded path by pointing it at a `testdata/` fixture rather than at another machine-specific path.

## libvips dependency

- **Ubuntu/local dev**: `./ubuntu_req` (`libvips42`, `libvips-dev` from apt). Distro packages may be too old for some formats.
- **Lambda image**: `Dockerfile` starts from `public.ecr.aws/lambda/provided:al2` and compiles libwebp, libde265, x265, libheif, and libvips 8.12.2 **from source**, because the Amazon Linux 2 repos are too old. It also downloads a Go toolchain (`ARG GO_VERSION`) for the same reason. Expect a long build. Format support (JPEG/WEBP/PNG/GIF/HEIF/TIFF) is determined entirely by which `-devel` packages and source builds are present when libvips is configured — adding a format means editing the Dockerfile, not the Go code.
- The final `go build` uses `-ldflags "-r /usr/local/lib"` so the runtime finds the shared libs installed under `/usr/local`.

**Gotcha:** the Dockerfile does `ADD photos.go ./` and builds `go build ... photos.go`, naming the single file. If you split the root module into multiple files, the container build silently misses them — update both the `ADD` and the `go build` line.

## How processing works

`processImage` (photos.go) is the shared core for both run modes:

1. `img.Process` with `StripMetadata: true`, `NoAutoRotate: false`, `Type: bimg.JPEG` — this single step drops EXIF, applies EXIF orientation, and normalizes to JPEG. Result is written as `orig.jpeg` and is the **source for every subsequent derivative**, so resizes never re-read the original file.
2. For each (dimension, format) pair: `smartDims` fits the original inside the max box preserving aspect ratio (never upscales — returns original dims if already smaller), then resize + convert, saved as `<name>.<ext>`.
3. `Thumbnail(thumbSize)` → `thumbnail.jpeg` (bimg's `Thumbnail` takes a single size for both dimensions).

Output filenames come from the *keys* of the dims map, which are the CSS breakpoint names ("1200", "992", …) rather than the pixel widths — the site uses them in `<picture>`/`<source media="(min-width:1200px)">`. Keep that naming convention when changing defaults.

`saveImageLocal` deliberately ignores `bimg.Write`'s error; several call sites in `Handler` and `processImage` are similarly loose about errors. Follow the existing style only if asked — otherwise prefer propagating.

## Lambda contract

`Handler` reads the bucket/key from `event.Records[0]` only (single record per invocation is assumed).

Key layout is preserved between buckets: source `<src>/media/images/tags/bread/orig.jpg` produces `<DESTINATION_BUCKET>/media/images/tags/bread/{1200,992,…}.{jpeg,webp}` plus `orig.jpeg` and `thumbnail.jpeg`. `splitKey` does the prefix/filename split; the filename itself is discarded — the *directory* is the identity of the image set. `cleanup`'s `getDestinationPrefix` mirrors this exact logic for deletion.

Work happens in `/tmp/output` (the only writable path in Lambda), then every file in that directory is uploaded. The directory is not cleared between invocations, so stale files from a warm container's previous run would be re-uploaded under the new prefix.

Source and destination buckets **must differ**, or the `ObjectCreated` trigger recurses.

### Environment variables

| Var | Lambda | CLI flag | Default |
|---|---|---|---|
| `DESTINATION_BUCKET` | required | n/a (uses `--output`) | — |
| `DIMENSIONS` | optional | `--dims` | the six breakpoints in `getDefaultDims()` |
| `FORMATS` | optional | `--formats` | `jpeg,webp` |
| `THUMB_SIZE` | optional | `--thumbSize` | 128 |
| `MAX_KEYS` | required by `cleanup` | n/a | — |

`DIMENSIONS` format: `name1:width1,height1;name2:width2,height2`. Empty/unset falls back to defaults — `parseDims` and `parseImageTypes` treat `""` as "not configured" precisely because `os.Getenv` cannot distinguish unset from empty.

`cleanup` caps deletion at `MAX_KEYS` from a single `ListObjectsV2` page and does not paginate; an image folder with more objects than that is only partly cleaned.
