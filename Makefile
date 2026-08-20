# nnr-photos - pure Go, no cgo, no libvips.
#
# The Lambda ships as a zip on provided.al2023; there is no container image
# and no ECR repository any more.

GOFLAGS  := -tags "lambda.norpc nodynamic" -trimpath -ldflags="-s -w"
ARCH     ?= arm64
ZIP      := photos-lambda.zip

.PHONY: all build test vet lambda clean fmt

all: build test

## build: local CLI used by the Django dev server (recipes/signals.py)
build:
	CGO_ENABLED=0 go build -tags nodynamic -o build/photos .

## lambda: stripped static binary + deployment zip
lambda: clean-zip
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build $(GOFLAGS) -o bootstrap .
	@if command -v zip >/dev/null 2>&1; then \
		zip -qj $(ZIP) bootstrap; \
	else \
		python3 -c "import zipfile; z=zipfile.ZipFile('$(ZIP)','w'); i=zipfile.ZipInfo('bootstrap'); i.compress_type=zipfile.ZIP_DEFLATED; i.external_attr=0o100755<<16; z.writestr(i, open('bootstrap','rb').read()); z.close()"; \
	fi
	@rm -f bootstrap
	@ls -la $(ZIP) | awk '{printf "%s  %.2f MB\n", $$9, $$5/1048576}'

## deploy: push the zip to an existing function (NAME=nnr-photos)
NAME ?= nnr-photos
deploy: lambda
	aws lambda update-function-code --function-name $(NAME) --zip-file fileb://$(ZIP)

test:
	go test -tags nodynamic ./...
	cd cleanup && go test ./...

vet:
	go vet -tags nodynamic ./...
	cd cleanup && go vet ./...

fmt:
	gofmt -w *.go cleanup/*.go

clean: clean-zip
	rm -rf build bootstrap

clean-zip:
	@rm -f $(ZIP)
