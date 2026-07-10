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
	"strings"
	"testing"
	"time"
)

const testToken = "0123456789abcdef"

func encodeTestJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	source := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			source.Set(x, y, color.NRGBA{R: uint8(x % 255), G: uint8(y % 255), B: 100, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, source, nil); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func testConfig(framePath string) config {
	return config{
		readToken:      testToken,
		uploadToken:    "fedcba9876543210",
		framePath:      framePath,
		mode:           "cover",
		quality:        85,
		maxUploadBytes: 15 * 1024 * 1024,
		maxImagePixels: 25 * 1000 * 1000,
	}
}

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
	directory := t.TempDir()
	server := &server{config: testConfig(filepath.Join(directory, "frame.jpg"))}
	meta, err := server.publish(encodeTestJPEG(t, 8, 8))
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
		config:                config{readToken: testToken, nextPollSeconds: 3600},
		meta:                  metadata{revision: "abc123", updatedAt: updated, frameBytes: 4567},
		manifestRequests:      7,
		lastManifestRequestAt: updated,
		frameRequests:         3,
		lastFrameRequestAt:    updated,
	}

	unauthorized := httptest.NewRecorder()
	server.status(unauthorized, httptest.NewRequest(http.MethodGet, "/status", nil))
	if unauthorized.Code != http.StatusNotFound {
		t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusNotFound)
	}

	response := httptest.NewRecorder()
	server.status(response, httptest.NewRequest(http.MethodGet, "/status?token="+testToken, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got, want := response.Body.String(), "\"ready\":true"; !bytes.Contains([]byte(got), []byte(want)) {
		t.Fatalf("status body = %s, want %s", got, want)
	}
	if got, want := response.Body.String(), "\"frame_bytes\":4567"; !bytes.Contains([]byte(got), []byte(want)) {
		t.Fatalf("status body = %s, want %s", got, want)
	}
	if got, want := response.Body.String(), "\"manifest_requests\":7"; !bytes.Contains([]byte(got), []byte(want)) {
		t.Fatalf("status body = %s, want %s", got, want)
	}
	if got, want := response.Body.String(), "\"frame_requests\":3"; !bytes.Contains([]byte(got), []byte(want)) {
		t.Fatalf("status body = %s, want %s", got, want)
	}
}

func TestManifestAndFrameUpdateRequestMetrics(t *testing.T) {
	directory := t.TempDir()
	server := &server{
		config: testConfig(filepath.Join(directory, "frame.jpg")),
		meta:   metadata{revision: "abc123", updatedAt: time.Now(), frameBytes: 4},
	}
	if err := os.WriteFile(server.config.framePath, []byte("jpeg"), 0600); err != nil {
		t.Fatal(err)
	}

	manifestResponse := httptest.NewRecorder()
	server.manifest(manifestResponse,
		httptest.NewRequest(http.MethodGet, "/manifest?token="+testToken, nil))
	frameResponse := httptest.NewRecorder()
	server.frame(frameResponse,
		httptest.NewRequest(http.MethodGet, "/frame.jpg?token="+testToken, nil))

	server.mu.RLock()
	defer server.mu.RUnlock()
	if server.manifestRequests != 1 || server.lastManifestRequestAt.IsZero() {
		t.Fatalf("manifest metrics = %d, %v", server.manifestRequests, server.lastManifestRequestAt)
	}
	if server.frameRequests != 1 || server.lastFrameRequestAt.IsZero() {
		t.Fatalf("frame metrics = %d, %v", server.frameRequests, server.lastFrameRequestAt)
	}
}

func TestUploadRequiresSeparateBearerToken(t *testing.T) {
	server := &server{config: testConfig(filepath.Join(t.TempDir(), "frame.jpg"))}
	encoded := encodeTestJPEG(t, 8, 8)

	readTokenRequest := httptest.NewRequest(http.MethodPost, "/api/frame?token="+testToken, bytes.NewReader(encoded))
	readTokenRequest.Header.Set("Content-Type", "image/jpeg")
	readTokenResponse := httptest.NewRecorder()
	server.upload(readTokenResponse, readTokenRequest)
	if readTokenResponse.Code != http.StatusNotFound {
		t.Fatalf("read token upload status = %d, want %d", readTokenResponse.Code, http.StatusNotFound)
	}

	uploadRequest := httptest.NewRequest(http.MethodPost, "/api/frame", bytes.NewReader(encoded))
	uploadRequest.Header.Set("Content-Type", "image/jpeg")
	uploadRequest.Header.Set("Authorization", "Bearer "+server.config.uploadToken)
	uploadResponse := httptest.NewRecorder()
	server.upload(uploadResponse, uploadRequest)
	if uploadResponse.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want %d: %s", uploadResponse.Code, http.StatusCreated, uploadResponse.Body.String())
	}
	if _, err := os.Stat(server.config.framePath); err != nil {
		t.Fatalf("published frame: %v", err)
	}
}

func TestLegacyUploadQueryRemainsCompatible(t *testing.T) {
	server := &server{config: testConfig(filepath.Join(t.TempDir(), "frame.jpg"))}
	server.config.uploadToken = testToken
	server.config.legacyUploadQuery = true
	request := httptest.NewRequest(http.MethodPost, "/api/frame?token="+testToken,
		bytes.NewReader(encodeTestJPEG(t, 4, 4)))
	request.Header.Set("Content-Type", "image/jpeg")
	response := httptest.NewRecorder()
	server.upload(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("legacy upload status = %d, want %d", response.Code, http.StatusCreated)
	}
}

func TestPublishRejectsExcessivePixelCount(t *testing.T) {
	server := &server{config: testConfig(filepath.Join(t.TempDir(), "frame.jpg"))}
	server.config.maxImagePixels = 100
	_, err := server.publish(encodeTestJPEG(t, 20, 20))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("publish error = %v, want pixel limit error", err)
	}
}

func TestPublishDoesNotRewriteIdenticalFrame(t *testing.T) {
	server := &server{config: testConfig(filepath.Join(t.TempDir(), "frame.jpg"))}
	encoded := encodeTestJPEG(t, 8, 8)
	first, err := server.publish(encoded)
	if err != nil {
		t.Fatal(err)
	}
	oldTime := time.Date(2020, time.January, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(server.config.framePath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	second, err := server.publish(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if first.revision != second.revision {
		t.Fatalf("revision changed: %s != %s", first.revision, second.revision)
	}
	info, err := os.Stat(server.config.framePath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(oldTime) {
		t.Fatalf("identical frame was rewritten at %s", info.ModTime())
	}
}

func TestUploadRejectsKnownOversizedBodyBeforeReading(t *testing.T) {
	server := &server{config: testConfig(filepath.Join(t.TempDir(), "frame.jpg"))}
	server.config.maxUploadBytes = 2
	request := httptest.NewRequest(http.MethodPost, "/api/frame", strings.NewReader("too large"))
	request.Header.Set("Content-Type", "image/jpeg")
	request.Header.Set("Authorization", "Bearer "+server.config.uploadToken)
	response := httptest.NewRecorder()
	server.upload(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("upload status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}
