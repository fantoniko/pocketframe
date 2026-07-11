package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
		readToken:               testToken,
		uploadToken:             "fedcba9876543210",
		framePath:               framePath,
		mode:                    "cover",
		quality:                 85,
		maxUploadBytes:          15 * 1024 * 1024,
		maxImagePixels:          25 * 1000 * 1000,
		nextPollSeconds:         3600,
		retryAfterSeconds:       1800,
		schedulePeriodSeconds:   3600,
		readDelaySeconds:        300,
		missingSlotRetrySeconds: 300,
		metadataPath:            framePath + ".json",
	}
}

func setPublicationHeaders(request *http.Request, slot int64, readDelay int) {
	id := "pocketframe-" + strconv.FormatInt(slot, 10)
	request.Header.Set("X-PocketFrame-Publication-Id", id)
	request.Header.Set("X-PocketFrame-Publication-Slot", strconv.FormatInt(slot, 10))
	request.Header.Set("X-PocketFrame-Scheduled-At", time.Unix(slot, 0).UTC().Format(time.RFC3339))
	request.Header.Set("X-PocketFrame-Read-Delay-Seconds", strconv.Itoa(readDelay))
	request.Header.Set("Idempotency-Key", id)
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
		config:                testConfig(filepath.Join(t.TempDir(), "frame.jpg")),
		meta:                  metadata{revision: "abc123", publicationID: "pocketframe-1783684800", publicationSlot: 1783684800, scheduledAt: updated, publishedAt: updated, readyAt: updated, idempotencyKey: "pocketframe-1783684800", frameBytes: 4567},
		now:                   func() time.Time { return updated.Add(5 * time.Minute) },
		startedAt:             updated,
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
	if got, want := response.Body.String(), "\"publication_state\":\"ready\""; !strings.Contains(got, want) {
		t.Fatalf("status body = %s, want %s", got, want)
	}
	if got, want := response.Body.String(), "\"server_time\":\"2026-07-10T12:05:00Z\""; !strings.Contains(got, want) {
		t.Fatalf("status body = %s, want %s", got, want)
	}
}

func TestManifestAndFrameUpdateRequestMetrics(t *testing.T) {
	directory := t.TempDir()
	server := &server{
		config: testConfig(filepath.Join(directory, "frame.jpg")),
		meta:   metadata{revision: "abc123", publicationID: "current", publicationSlot: 1783684800, scheduledAt: time.Unix(1783684800, 0), publishedAt: time.Unix(1783684800, 0), readyAt: time.Unix(1783684800, 0), idempotencyKey: "current", frameBytes: 4},
		now:    func() time.Time { return time.Unix(1783685100, 0).UTC() },
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

func TestUploadStoresPublicationMetadata(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	framePath := filepath.Join(t.TempDir(), "frame.jpg")
	appServer := &server{config: testConfig(framePath), now: func() time.Time { return now }}
	request := httptest.NewRequest(http.MethodPost, "/api/frame", bytes.NewReader(encodeTestJPEG(t, 8, 8)))
	request.Header.Set("Content-Type", "image/jpeg")
	request.Header.Set("Authorization", "Bearer "+appServer.config.uploadToken)
	setPublicationHeaders(request, now.Unix(), 300)
	response := httptest.NewRecorder()

	appServer.upload(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	for _, expected := range []string{
		"publication_id=pocketframe-" + strconv.FormatInt(now.Unix(), 10),
		"publication_slot=" + strconv.FormatInt(now.Unix(), 10),
		"published_at=" + strconv.FormatInt(now.Unix(), 10),
		"ready_at=" + strconv.FormatInt(now.Add(5*time.Minute).Unix(), 10),
		"update_ready=0",
	} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("manifest %q does not contain %q", response.Body.String(), expected)
		}
	}
	reloaded := &server{config: testConfig(framePath), now: func() time.Time { return now }}
	if err := reloaded.loadExistingFrame(); err != nil {
		t.Fatal(err)
	}
	if reloaded.meta.publicationID != "pocketframe-"+strconv.FormatInt(now.Unix(), 10) ||
		reloaded.meta.readyAt != now.Add(5*time.Minute) {
		t.Fatalf("reloaded metadata = %#v", reloaded.meta)
	}
}

func TestUploadIsIdempotentForPublicationSlot(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	server := &server{config: testConfig(filepath.Join(t.TempDir(), "frame.jpg")), now: func() time.Time { return now }}
	first := httptest.NewRequest(http.MethodPost, "/api/frame", bytes.NewReader(encodeTestJPEG(t, 8, 8)))
	first.Header.Set("Content-Type", "image/jpeg")
	first.Header.Set("Authorization", "Bearer "+server.config.uploadToken)
	setPublicationHeaders(first, now.Unix(), 300)
	firstResponse := httptest.NewRecorder()
	server.upload(firstResponse, first)
	if firstResponse.Code != http.StatusCreated {
		t.Fatalf("first upload = %d: %s", firstResponse.Code, firstResponse.Body.String())
	}
	firstMeta := server.meta

	retry := httptest.NewRequest(http.MethodPost, "/api/frame", strings.NewReader("not an image"))
	retry.Header.Set("Content-Type", "image/jpeg")
	retry.Header.Set("Authorization", "Bearer "+server.config.uploadToken)
	setPublicationHeaders(retry, now.Unix(), 300)
	retryResponse := httptest.NewRecorder()
	server.upload(retryResponse, retry)

	if retryResponse.Code != http.StatusOK {
		t.Fatalf("idempotent retry = %d, want %d: %s", retryResponse.Code, http.StatusOK, retryResponse.Body.String())
	}
	if server.meta != firstMeta {
		t.Fatalf("metadata changed on retry: %#v != %#v", server.meta, firstMeta)
	}
}

func TestSameJPEGWithNewPublicationIDIsNewPublication(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	server := &server{config: testConfig(filepath.Join(t.TempDir(), "frame.jpg")), now: func() time.Time { return now }}
	encoded := encodeTestJPEG(t, 8, 8)
	upload := func(slot time.Time) metadata {
		now = slot
		request := httptest.NewRequest(http.MethodPost, "/api/frame", bytes.NewReader(encoded))
		request.Header.Set("Content-Type", "image/jpeg")
		request.Header.Set("Authorization", "Bearer "+server.config.uploadToken)
		setPublicationHeaders(request, slot.Unix(), 300)
		response := httptest.NewRecorder()
		server.upload(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("upload at %s = %d: %s", slot, response.Code, response.Body.String())
		}
		return server.meta
	}
	first := upload(now)
	second := upload(now.Add(time.Hour))
	if first.revision != second.revision {
		t.Fatalf("identical JPEG revision changed: %s != %s", first.revision, second.revision)
	}
	if first.publicationID == second.publicationID || second.publicationSlot-first.publicationSlot != 3600 {
		t.Fatalf("publication was not advanced: %#v -> %#v", first, second)
	}
}

func TestManifestBeforeAndAfterReadyAt(t *testing.T) {
	slot := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	now := slot.Add(4 * time.Minute)
	server := &server{
		config: testConfig(filepath.Join(t.TempDir(), "frame.jpg")),
		now:    func() time.Time { return now },
		meta: metadata{
			revision: "abc", publicationID: "pocketframe-" + strconv.FormatInt(slot.Unix(), 10),
			publicationSlot: slot.Unix(), scheduledAt: slot, publishedAt: slot,
			readyAt: slot.Add(5 * time.Minute), idempotencyKey: "pocketframe-" + strconv.FormatInt(slot.Unix(), 10),
		},
	}
	before := httptest.NewRecorder()
	server.manifest(before, httptest.NewRequest(http.MethodGet, "/manifest?token="+testToken, nil))
	if !strings.Contains(before.Body.String(), "update_ready=0") || !strings.Contains(before.Body.String(), "next_poll_seconds=300") {
		t.Fatalf("manifest before ready_at = %q", before.Body.String())
	}

	now = slot.Add(5 * time.Minute)
	after := httptest.NewRecorder()
	server.manifest(after, httptest.NewRequest(http.MethodGet, "/manifest?token="+testToken, nil))
	if !strings.Contains(after.Body.String(), "update_ready=1") || !strings.Contains(after.Body.String(), "next_poll_seconds=3600") {
		t.Fatalf("manifest after ready_at = %q", after.Body.String())
	}
}

func TestManifestMissingCurrentSlotUsesShortRetry(t *testing.T) {
	oldSlot := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	now := oldSlot.Add(time.Hour + 5*time.Minute)
	server := &server{
		config: testConfig(filepath.Join(t.TempDir(), "frame.jpg")),
		now:    func() time.Time { return now },
		meta: metadata{
			revision: "old", publicationID: "pocketframe-" + strconv.FormatInt(oldSlot.Unix(), 10),
			publicationSlot: oldSlot.Unix(), scheduledAt: oldSlot, publishedAt: oldSlot,
			readyAt: oldSlot.Add(5 * time.Minute), idempotencyKey: "pocketframe-" + strconv.FormatInt(oldSlot.Unix(), 10),
		},
	}
	response := httptest.NewRecorder()
	server.manifest(response, httptest.NewRequest(http.MethodGet, "/manifest?token="+testToken, nil))
	if !strings.Contains(response.Body.String(), "update_ready=0") ||
		!strings.Contains(response.Body.String(), "retry_after_seconds=300") {
		t.Fatalf("missing-slot manifest = %q", response.Body.String())
	}
}

func TestNextPollUsesAbsoluteHourlySlot(t *testing.T) {
	slot := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	server := &server{config: testConfig(filepath.Join(t.TempDir(), "frame.jpg"))}
	meta := metadata{publicationSlot: slot.Unix(), readyAt: slot.Add(5 * time.Minute)}

	ready, nextPoll, _ := server.manifestTiming(meta, slot.Add(23*time.Minute))
	if !ready || nextPoll != 42*60 {
		t.Fatalf("12:23 timing = ready %v, next %d; want true, 2520", ready, nextPoll)
	}
	ready, nextPoll, _ = server.manifestTiming(meta, slot.Add(59*time.Minute+50*time.Second))
	if !ready || nextPoll != 5*60+10 {
		t.Fatalf("12:59:50 timing = ready %v, next %d; want true, 310", ready, nextPoll)
	}
}

func TestStructuredLogsDescribeEventsWithoutLeakingTokens(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	server := &server{
		config: testConfig(filepath.Join(t.TempDir(), "frame.jpg")),
		now:    func() time.Time { return now },
		logger: logger,
	}

	unauthorized := httptest.NewRecorder()
	server.status(unauthorized, httptest.NewRequest(http.MethodGet,
		"/status?token=read-secret-that-must-not-appear", nil))

	upload := httptest.NewRequest(http.MethodPost, "/api/frame",
		bytes.NewReader(encodeTestJPEG(t, 8, 8)))
	upload.Header.Set("Content-Type", "image/jpeg")
	upload.Header.Set("Authorization", "Bearer "+server.config.uploadToken)
	setPublicationHeaders(upload, now.Unix(), 300)
	response := httptest.NewRecorder()
	server.upload(response, upload)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload = %d: %s", response.Code, response.Body.String())
	}

	output := logs.String()
	for _, expected := range []string{
		`"event":"authorization_rejected"`,
		`"path":"/status"`,
		`"event":"upload_accepted"`,
		`"publication_id":"pocketframe-` + strconv.FormatInt(now.Unix(), 10) + `"`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("logs %q do not contain %q", output, expected)
		}
	}
	for _, secret := range []string{
		"read-secret-that-must-not-appear",
		server.config.uploadToken,
		"?token=",
	} {
		if strings.Contains(output, secret) {
			t.Fatalf("logs leaked %q: %s", secret, output)
		}
	}
}

func TestNewLoggerRejectsUnknownLevel(t *testing.T) {
	if _, err := newLogger("verbose"); err == nil {
		t.Fatal("newLogger accepted an unknown level")
	}
}
