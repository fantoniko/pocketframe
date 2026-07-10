package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrepareProducesPocketBookGrayscale(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 3, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 3; x++ {
			source.Set(x, y, color.NRGBA{R: 255, G: 64, B: 32, A: 255})
		}
	}

	prepared := prepare(source, "cover")
	if prepared.Bounds().Dx() != targetWidth || prepared.Bounds().Dy() != targetHeight {
		t.Fatalf("prepared size = %v, want %dx%d", prepared.Bounds(), targetWidth, targetHeight)
	}
	if got := prepared.GrayAt(targetWidth/2, targetHeight/2).Y; got == 0 || got == 255 {
		t.Fatalf("unexpected grayscale conversion value: %d", got)
	}
}

func TestPrepareContainPreservesWhiteMargins(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	for x := 0; x < 4; x++ {
		source.Set(x, 0, color.NRGBA{R: 0, G: 0, B: 0, A: 255})
	}

	prepared := prepare(source, "contain")
	if got := prepared.GrayAt(0, 0).Y; got != 255 {
		t.Fatalf("top margin = %d, want white", got)
	}
	if got := prepared.GrayAt(targetWidth/2, targetHeight/2).Y; got != 0 {
		t.Fatalf("center = %d, want black", got)
	}
}

func TestOrientRotateClockwise(t *testing.T) {
	source := image.NewGray(image.Rect(0, 0, 2, 3))
	source.SetGray(0, 0, color.Gray{Y: 10})
	source.SetGray(1, 0, color.Gray{Y: 20})
	source.SetGray(0, 1, color.Gray{Y: 30})
	source.SetGray(1, 1, color.Gray{Y: 40})
	source.SetGray(0, 2, color.Gray{Y: 50})
	source.SetGray(1, 2, color.Gray{Y: 60})

	rotated := orient(source, 6)
	if got, want := rotated.Bounds(), image.Rect(0, 0, 3, 2); got != want {
		t.Fatalf("rotated bounds = %v, want %v", got, want)
	}
	if got := color.GrayModel.Convert(rotated.At(2, 0)).(color.Gray).Y; got != 10 {
		t.Fatalf("rotated top right = %d, want 10", got)
	}
	if got := color.GrayModel.Convert(rotated.At(0, 1)).(color.Gray).Y; got != 60 {
		t.Fatalf("rotated bottom left = %d, want 60", got)
	}
}

func TestPublishCreatesPreparedFrame(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			source.Set(x, y, color.NRGBA{R: uint8(x * 20), G: uint8(y * 20), B: 100, A: 255})
		}
	}

	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, source, nil); err != nil {
		t.Fatal(err)
	}

	directory := t.TempDir()
	server := &server{config: config{
		framePath: filepath.Join(directory, "frame.jpg"),
		mode:      "cover",
		quality:   85,
	}}
	meta, err := server.publish(encoded.Bytes())
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(meta.revision) != 64 {
		t.Fatalf("revision length = %d, want SHA-256", len(meta.revision))
	}

	file, err := os.Open(server.config.framePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	prepared, err := jpeg.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.Bounds(); got.Dx() != targetWidth || got.Dy() != targetHeight {
		t.Fatalf("published size = %v, want %dx%d", got, targetWidth, targetHeight)
	}
	pixel := color.NRGBAModel.Convert(prepared.At(targetWidth/2, targetHeight/2)).(color.NRGBA)
	if pixel.R != pixel.G || pixel.G != pixel.B {
		t.Fatalf("published pixel is not grayscale: %#v", pixel)
	}
}

func TestIsSupportedImage(t *testing.T) {
	for _, contentType := range []string{"image/jpeg", "image/png; charset=binary", "image/gif"} {
		if !isSupportedImage(contentType) {
			t.Fatalf("expected supported content type: %s", contentType)
		}
	}
	if isSupportedImage("image/webp") {
		t.Fatal("unexpected WebP support")
	}
}

func TestStatusRequiresTokenAndReturnsFrameMetadata(t *testing.T) {
	updated := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	server := &server{
		config: config{token: "0123456789abcdef", nextPollSeconds: 3600},
		meta:   metadata{revision: "abc123", updatedAt: updated, frameBytes: 4567},
	}

	unauthorized := httptest.NewRecorder()
	server.status(unauthorized, httptest.NewRequest(http.MethodGet, "/status", nil))
	if unauthorized.Code != http.StatusNotFound {
		t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusNotFound)
	}

	response := httptest.NewRecorder()
	server.status(response, httptest.NewRequest(http.MethodGet, "/status?token=0123456789abcdef", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got, want := response.Body.String(), "\"ready\":true"; !bytes.Contains([]byte(got), []byte(want)) {
		t.Fatalf("status body = %s, want %s", got, want)
	}
	if got, want := response.Body.String(), "\"frame_bytes\":4567"; !bytes.Contains([]byte(got), []byte(want)) {
		t.Fatalf("status body = %s, want %s", got, want)
	}
}
