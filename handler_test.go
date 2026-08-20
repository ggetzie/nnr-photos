package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestSplitKey(t *testing.T) {
	tests := []struct {
		key        string
		wantPrefix string
		wantFile   string
		wantErr    bool
	}{
		{"media/images/tags/bread/orig.jpg", "media/images/tags/bread", "orig.jpg", false},
		{"a/b.jpg", "a", "b.jpg", false},
		// Previously sliced key[0:-1] and panicked at runtime.
		{"noslash.jpg", "", "noslash.jpg", false},
		{"noslash", "", "noslash", false},
		{"a/", "", "", true},
		{"media/images/tags/bread/", "", "", true},
		{"", "", "", true},
		{"/", "", "", true},
		{"/leading.jpg", "", "leading.jpg", false},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			prefix, file, err := splitKey(tc.key)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("splitKey(%q) = (%q, %q, nil), want error", tc.key, prefix, file)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitKey(%q) unexpected error: %v", tc.key, err)
			}
			if prefix != tc.wantPrefix || file != tc.wantFile {
				t.Errorf("splitKey(%q) = (%q, %q), want (%q, %q)", tc.key, prefix, file, tc.wantPrefix, tc.wantFile)
			}
		})
	}
}

// TestSplitKeyNoPanic is an explicit regression guard: any key at all must
// return, never panic.
func TestSplitKeyNoPanic(t *testing.T) {
	for _, k := range []string{"", "/", "//", "a", "a/", "/a", "a//b", "....", "a/b/c/d/e.jpg"} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("splitKey(%q) panicked: %v", k, r)
				}
			}()
			_, _, _ = splitKey(k)
		}()
	}
}

// fakeS3 records what was uploaded so the handler can be tested end to end.
type fakeS3 struct {
	object []byte
	getErr error
	puts   []*s3.PutObjectInput
	bodies map[string][]byte
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(f.object))}, nil
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if f.bodies == nil {
		f.bodies = map[string][]byte{}
	}
	body, _ := io.ReadAll(in.Body)
	f.bodies[*in.Key] = body
	f.puts = append(f.puts, in)
	return &s3.PutObjectOutput{}, nil
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Pix[m.PixOffset(x, y)+0] = uint8(x % 256)
			m.Pix[m.PixOffset(x, y)+1] = uint8(y % 256)
			m.Pix[m.PixOffset(x, y)+2] = uint8((x + y) % 256)
			m.Pix[m.PixOffset(x, y)+3] = 255
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestHandleRecordUploadsEverything(t *testing.T) {
	fake := &fakeS3{object: testPNG(t, 1600, 1200)}
	cfg := settings{
		destinationBucket: "dest",
		dims:              getDefaultDims(),
		formats:           getDefaultImageTypes(),
		thumbSize:         defaultThumbSize,
	}
	rec := events.S3EventRecord{}
	rec.S3.Bucket.Name = "src"
	rec.S3.Object.Key = "media/images/tags/bread/orig.png"

	if err := handleRecord(context.Background(), fake, cfg, rec); err != nil {
		t.Fatal(err)
	}

	if len(fake.puts) != 14 {
		t.Errorf("uploaded %d objects, want 14", len(fake.puts))
	}
	want := map[string]string{
		"media/images/tags/bread/orig.jpeg":      "image/jpeg",
		"media/images/tags/bread/thumbnail.jpeg": "image/jpeg",
		"media/images/tags/bread/1200.jpeg":      "image/jpeg",
		"media/images/tags/bread/1200.webp":      "image/webp",
		"media/images/tags/bread/320.webp":       "image/webp",
	}
	got := map[string]string{}
	for _, p := range fake.puts {
		got[*p.Key] = *p.ContentType
		if p.CacheControl == nil || *p.CacheControl == "" {
			t.Errorf("%s: no CacheControl set", *p.Key)
		}
	}
	for key, ct := range want {
		if got[key] != ct {
			t.Errorf("key %s: ContentType %q, want %q", key, got[key], ct)
		}
	}
}

// TestHandleRecordDecodesURLEncodedKey covers keys with spaces, which S3
// delivers percent/plus-encoded and the old code passed through verbatim.
func TestHandleRecordDecodesURLEncodedKey(t *testing.T) {
	fake := &fakeS3{object: testPNG(t, 400, 300)}
	cfg := settings{
		destinationBucket: "dest",
		dims:              map[string]ImageSize{"320": {310, 225}},
		formats:           []ImageFormat{FormatJPEG},
		thumbSize:         64,
	}
	rec := events.S3EventRecord{}
	rec.S3.Bucket.Name = "src"
	// S3 escapes within path segments and leaves the slashes alone.
	rec.S3.Object.Key = "media/images/tags/my+bread/orig.png"

	if err := handleRecord(context.Background(), fake, cfg, rec); err != nil {
		t.Fatal(err)
	}
	for _, p := range fake.puts {
		if got := *p.Key; got[:len("media/images/tags/my bread/")] != "media/images/tags/my bread/" {
			t.Errorf("key %q was not decoded", got)
		}
	}
}

func TestHandlerRejectsEmptyRecords(t *testing.T) {
	if _, err := Handler(context.Background(), events.S3Event{}); err == nil {
		t.Error("Handler with no records succeeded, want error")
	}
}

func TestHandleRecordPropagatesDownloadError(t *testing.T) {
	fake := &fakeS3{getErr: errors.New("boom")}
	cfg := settings{destinationBucket: "dest", dims: getDefaultDims(), formats: getDefaultImageTypes(), thumbSize: 128}
	rec := events.S3EventRecord{}
	rec.S3.Object.Key = "a/b.png"
	if err := handleRecord(context.Background(), fake, cfg, rec); err == nil {
		t.Error("expected download error to propagate")
	}
}

// TestHandleRecordRejectsNonImage: a processing failure must not report success.
func TestHandleRecordRejectsNonImage(t *testing.T) {
	fake := &fakeS3{object: []byte("this is not an image")}
	cfg := settings{destinationBucket: "dest", dims: getDefaultDims(), formats: getDefaultImageTypes(), thumbSize: 128}
	rec := events.S3EventRecord{}
	rec.S3.Object.Key = "a/b.txt"
	if err := handleRecord(context.Background(), fake, cfg, rec); err == nil {
		t.Error("expected a non-image to fail")
	}
	if len(fake.puts) != 0 {
		t.Errorf("uploaded %d objects for a non-image, want 0", len(fake.puts))
	}
}
