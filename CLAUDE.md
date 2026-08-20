# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`nnr-photos` optimizes images for [No Nonsense Recipes](https://nononsense.recipes). It is one Go program with two run modes selected by the `--local` flag in `main()`:

- **no flag** → `lambda.Start(Handler)`, an AWS Lambda triggered by an S3 `ObjectCreated` event
- **`--local`** → CLI that reads one file from disk and writes the output set to a directory

**There is no cgo and no native dependency.** Image work is stdlib `image/*` plus `golang.org/x/image` (TIFF decode, `draw` for resampling), `github.com/gen2brain/webp` (WebP encode/decode) and `github.com/gen2brain/heic` (HEIC decode). Both gen2brain packages are CGo-free. The program builds with `CGO_ENABLED=0` and ships as a ~5.5 MB zip on `provided.al2023`.

This replaced a bimg/libvips implementation that needed a >1 GB container image. Do not reintroduce a native image library without a strong reason.

## Two separate Go modules

The repo contains two independent modules with **no `go.work` and no parent/child relationship**:

| Path | Module | Purpose |
|---|---|---|
| `.` | `github.com/ggetzie/nnr-photos` | the optimizer |
| `cleanup/` | `github.com/ggetzie/nnr-photos/cleanup` | separate Lambda; on `ObjectRemoved`, deletes the whole derived-image folder from the destination bucket |

`go build ./...` from the repo root does **not** include `cleanup/`. Run go commands from inside whichever module you are changing, or use the Makefile targets, which cover both.

## Commands

```bash
make build        # local CLI -> build/photos  (Django dev server depends on this path)
make test         # both modules
make vet          # both modules
make lambda       # stripped arm64 bootstrap + photos-lambda.zip
make deploy NAME=nnr-photos
```

Single test: `go test -tags nodynamic -run TestSmartDims .`

**Always pass `-tags nodynamic`.** Without it, `gen2brain/webp` and `gen2brain/heic` probe for a system libwebp/libheif via `purego`/`dlopen` at runtime. On Lambda that is a pointless syscall on every cold start and a source of nondeterminism. The Makefile sets it; ad-hoc `go test`/`go build` invocations must too.

Local run:

```bash
./build/photos --local --input=/path/in.png --output=/path/outdir \
  --dims="web:800,600;mobile:400,300" --formats="jpeg,webp" --thumbSize=64
```

`s3_test.json` is a sample S3 `ObjectCreated` event (`aws lambda invoke --payload fileb://s3_test.json`).

## File layout

| File | Contents |
|---|---|
| `main.go` | flag parsing, `--local` mode |
| `handler.go` | `Handler`, `handleRecord`, S3 get/put, `splitKey`, `loadSettings` |
| `process.go` | `processImage` - the shared core for both run modes |
| `decode.go` | format registration, `decodeImage`, EXIF orientation |
| `resize.go` | `resizeTo`, `flatten`, `coverCrop` |
| `encode.go` | `encode` - the single seam for the encoder stack |
| `dims.go` | `smartDims`, `parseDims`, `parseImageTypes`, `buildPath` |
| `types.go` | `ImageSize`, `ImageFormat`, `Derivative`, `sortedDims` |

The build is `go build .`, not a named file, so adding a file needs no build change.

## How processing works

`processImage` (process.go) is the shared core:

1. The caller decodes once via `decodeImage`, which sniffs the format, enforces the 40 MP ceiling, and applies EXIF orientation.
2. `flatten` composites alpha onto **white** once. Everything downstream descends from this single image.
3. `orig.jpeg` is encoded at the original dimensions, quality 75.
4. Breakpoints are walked **largest first** (`sortedDims`) and each resize feeds the next. `smartDims` is always computed against the *original* dimensions so "never upscale" is preserved exactly.
5. `coverCrop` produces a centre-cropped square `thumbnail.jpeg` at **quality 95**.

`processImage` returns `[]Derivative` (name, format, bytes) rather than writing files. `Handler` uploads them from `bytes.Reader`; `--local` writes them to disk. **Nothing touches `/tmp`.**

Output filenames come from the *keys* of the dims map, which are CSS breakpoint names ("1200", "992", …) rather than pixel widths — the site uses them in `<picture>`/`<source media="(min-width:1200px)">`. Keep that naming convention when changing defaults.

### Things that look wrong but are deliberate

- **`thumbnail.jpeg` is quality 95** while everything else is 75. The old `bimg.Thumbnail` hardcoded `Quality: 95`; changing it would alter every thumbnail on the site. Pinned by `TestThumbnailIsHigherQuality`.
- **`coverCrop`'s no-enlarge guard is an AND** (`inW < size && inH < size`), so a 100x2000 image is upscaled before cropping. This mirrors bimg's arithmetic exactly.
- **`decode.go` registers the `mif1` HEIC brand itself.** `gen2brain/heic` registers `heic/heix/hevc/hevx/msf1` but not `mif1`, which libheif and several camera pipelines emit as the major brand — without it `image.Decode` cannot sniff those files even though the decoder handles them fine.
- **`golang.org/x/image/webp` must NOT be imported.** `gen2brain/webp` already registers the `webp` format and provides the encoder; importing both double-registers the name.
- **`resizeTo` must return `*image.RGBA` and use `draw.Src`.** `x/image/draw` only has generated fast paths for those; anything else drops to the per-pixel interface path and is ~10x slower.
- **The first HEIC decode in a container costs ~575 ms extra** while wazero compiles the embedded WASM module (measured: 808 ms first call, ~230 ms after). Only HEIC pays this; `gen2brain/webp` is transpiled Go, not a WASM runtime, so it has no equivalent warm-up.

## Lambda contract

`Handler` iterates **all** `event.Records`. Object keys are URL-decoded (`url.QueryUnescape`) — S3 delivers them percent/plus-encoded, and the old code 404'd on any key containing a space.

Key layout is preserved between buckets: source `<src>/media/images/tags/bread/orig.jpg` produces `<DESTINATION_BUCKET>/media/images/tags/bread/{1200,992,…}.{jpeg,webp}` plus `orig.jpeg` and `thumbnail.jpeg` — 14 files. `splitKey` does the prefix/filename split; the filename itself is discarded — the *directory* is the identity of the image set. `cleanup`'s `getDestinationPrefix` mirrors this logic.

Uploads set `ContentType` and a one-year immutable `CacheControl`.

Source and destination buckets **must differ**, or the `ObjectCreated` trigger recurses.

### Environment variables

| Var | Lambda | CLI flag | Default |
|---|---|---|---|
| `DESTINATION_BUCKET` | required (validated) | n/a (uses `--output`) | — |
| `DIMENSIONS` | optional | `--dims` | the six breakpoints in `getDefaultDims()` |
| `FORMATS` | optional | `--formats` | `jpeg,webp` |
| `THUMB_SIZE` | optional | `--thumbSize` | 128 |
| `MAX_KEYS` | required by `cleanup` | n/a | — |

`DIMENSIONS` format: `name1:width1,height1;name2:width2,height2`. Empty/unset falls back to defaults — `parseDims` and `parseImageTypes` treat `""` as "not configured" precisely because `os.Getenv` cannot distinguish unset from empty. A *malformed* value is now a hard error; it used to be logged and ignored, which silently produced 2 files instead of 14.

`cleanup` caps deletion at `MAX_KEYS` from a single `ListObjectsV2` page and does not paginate; an image folder with more objects than that is only partly cleaned.

## Coupling to the Django app

This repo is a git submodule of the parent Django project at `/usr/local/src/nnr`. Two things there constrain this code:

- **`recipes/models.py:178-179`** — `SCREEN_SIZES` and `PHOTO_EXTENSIONS` must match `getDefaultDims()` and `getDefaultImageTypes()`. They define the `<picture>` markup. `TestProcessImageManifest` pins the resulting 14-file set.
- **`recipes/signals.py:11`** — hardcodes `PHOTOS = "/usr/local/src/nnr/awslambda/photos/build/photos"` and shells out to `--local` on `post_save` when `DEBUG`, with no flags. So **the defaults are the production contract.**

HEIC uploads also require `pillow-heif` and `register_heif_opener()` on the Django side — without it `ImageField` rejects HEIC before it ever reaches S3.

## Tests

`go test -tags nodynamic ./...`. Fixtures live in `testdata/`:

- `testdata/orientation/{landscape,portrait}_{1..8}.jpg` — the standard EXIF orientation set. `TestOrientation` asserts each variant lands within a mean-absolute-error tolerance of the upright reference, which catches transpose/mirror mixups that dimension checks miss.
- `testdata/*.heic` — real HEIC files including `test.heic`, which is grid/tiled (how phones store photos) and `mif1`-branded files.

Everything else is generated synthetically in-test. Tests assert *behaviour* — manifest, dimensions, sniffed format, geometry — not encoder bytes, because encoder output drifts between versions.
