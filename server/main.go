package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	xdraw "golang.org/x/image/draw"
)

const (
	targetWidth  = 1404
	targetHeight = 1872
)

type config struct {
	readToken               string
	uploadToken             string
	legacyUploadQuery       bool
	framePath               string
	mode                    string
	quality                 int
	maxUploadBytes          int64
	maxImagePixels          int64
	nextPollSeconds         int
	retryAfterSeconds       int
	schedulePeriodSeconds   int
	scheduleOffsetSeconds   int
	readDelaySeconds        int
	missingSlotRetrySeconds int
	metadataPath            string
	logLevel                string
}

type metadata struct {
	revision        string
	publicationID   string
	publicationSlot int64
	scheduledAt     time.Time
	publishedAt     time.Time
	readyAt         time.Time
	idempotencyKey  string
	frameBytes      int64
}

type persistedMetadata struct {
	Revision        string `json:"revision"`
	PublicationID   string `json:"publication_id"`
	PublicationSlot int64  `json:"publication_slot"`
	ScheduledAt     int64  `json:"scheduled_at"`
	PublishedAt     int64  `json:"published_at"`
	ReadyAt         int64  `json:"ready_at"`
	IdempotencyKey  string `json:"idempotency_key"`
	FrameBytes      int64  `json:"frame_bytes"`
}

type publication struct {
	id             string
	slot           int64
	scheduledAt    time.Time
	readDelay      time.Duration
	idempotencyKey string
	receivedAt     time.Time
	legacy         bool
}

type statusResponse struct {
	Ready                   bool   `json:"ready"`
	ServerTime              string `json:"server_time"`
	UptimeSeconds           int64  `json:"uptime_seconds"`
	PublicationState        string `json:"publication_state"`
	ExpectedPublicationSlot int64  `json:"expected_publication_slot"`
	Revision                string `json:"revision,omitempty"`
	UpdatedAt               string `json:"updated_at,omitempty"`
	PublicationID           string `json:"publication_id,omitempty"`
	PublicationSlot         int64  `json:"publication_slot,omitempty"`
	ScheduledAt             string `json:"scheduled_at,omitempty"`
	ReadyAt                 string `json:"ready_at,omitempty"`
	UpdateReady             bool   `json:"update_ready"`
	FrameBytes              int64  `json:"frame_bytes,omitempty"`
	TargetWidth             int    `json:"target_width"`
	TargetHeight            int    `json:"target_height"`
	NextPollSeconds         int    `json:"next_poll_seconds"`
	ManifestRequests        uint64 `json:"manifest_requests"`
	LastManifestRequestAt   string `json:"last_manifest_request_at,omitempty"`
	FrameRequests           uint64 `json:"frame_requests"`
	LastFrameRequestAt      string `json:"last_frame_request_at,omitempty"`
}

type server struct {
	config                config
	mu                    sync.RWMutex
	uploadMu              sync.Mutex
	publishMu             sync.Mutex
	meta                  metadata
	manifestRequests      uint64
	lastManifestRequestAt time.Time
	frameRequests         uint64
	lastFrameRequestAt    time.Time
	now                   func() time.Time
	startedAt             time.Time
	logger                *slog.Logger
}

func main() {
	logger, err := newLogger(getenv("POCKETFRAME_LOG_LEVEL", "info"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	slog.SetDefault(logger)
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := runHealthcheck(); err != nil {
			slog.Error("healthcheck failed", "event", "healthcheck_failed", "error", err)
			os.Exit(1)
		}
		return
	}

	config, err := loadConfig()
	if err != nil {
		slog.Error("configuration rejected", "event", "configuration_rejected", "error", err)
		os.Exit(1)
	}

	server := &server{config: config, logger: logger, startedAt: time.Now().UTC()}
	if err := server.loadExistingFrame(); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error("existing frame rejected", "event", "frame_state_rejected", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", server.live)
	mux.HandleFunc("GET /healthz", server.health)
	mux.HandleFunc("GET /manifest", server.manifest)
	mux.HandleFunc("GET /status", server.status)
	mux.HandleFunc("GET /frame.jpg", server.frame)
	mux.HandleFunc("POST /api/frame", server.upload)

	address := ":" + getenv("PORT", "8080")
	slog.Info("PocketFrame server started",
		"event", "server_started", "address", address,
		"frame_ready", server.meta.revision != "", "mode", config.mode,
		"publication_state", server.publicationState(server.meta, server.currentTime()),
		"publication_id", server.meta.publicationID,
		"publication_slot", server.meta.publicationSlot,
		"revision", server.meta.revision,
		"quality", config.quality, "schedule_period_seconds", config.schedulePeriodSeconds,
		"schedule_offset_seconds", config.scheduleOffsetSeconds,
		"read_delay_seconds", config.readDelaySeconds,
		"missing_slot_retry_seconds", config.missingSlotRetrySeconds,
		"legacy_upload_query", config.legacyUploadQuery, "log_level", config.logLevel)
	httpServer := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- httpServer.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server stopped", "event", "server_failed", "error", err)
			os.Exit(1)
		}
	case signal := <-signals:
		slog.Info("shutdown requested", "event", "shutdown_requested", "signal", signal.String())
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			slog.Error("graceful shutdown failed", "event", "shutdown_failed", "error", err)
			return
		}
		slog.Info("PocketFrame server stopped", "event", "server_stopped")
	}
}

func newLogger(levelName string) (*slog.Logger, error) {
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(levelName)) {
	case "debug":
		level = slog.LevelDebug
	case "info", "":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, errors.New("POCKETFRAME_LOG_LEVEL must be debug, info, warn, or error")
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})), nil
}

func loadConfig() (config, error) {
	legacyToken := os.Getenv("POCKETFRAME_TOKEN")
	readToken := os.Getenv("POCKETFRAME_READ_TOKEN")
	if readToken == "" {
		readToken = legacyToken
	}
	uploadToken := os.Getenv("POCKETFRAME_UPLOAD_TOKEN")
	legacyUploadQuery := uploadToken == "" && legacyToken != ""
	if uploadToken == "" {
		uploadToken = legacyToken
	}
	if len(readToken) < 16 {
		return config{}, errors.New("POCKETFRAME_READ_TOKEN or POCKETFRAME_TOKEN must contain at least 16 characters")
	}
	if len(uploadToken) < 16 {
		return config{}, errors.New("POCKETFRAME_UPLOAD_TOKEN or POCKETFRAME_TOKEN must contain at least 16 characters")
	}

	mode := strings.ToLower(getenv("POCKETFRAME_MODE", "cover"))
	if mode != "cover" && mode != "contain" {
		return config{}, errors.New("POCKETFRAME_MODE must be cover or contain")
	}
	quality, err := getenvInt("POCKETFRAME_QUALITY", 85, 1, 100)
	if err != nil {
		return config{}, err
	}
	maxMegabytes, err := getenvInt("POCKETFRAME_MAX_UPLOAD_MB", 15, 1, 100)
	if err != nil {
		return config{}, err
	}
	maxImageMegapixels, err := getenvInt("POCKETFRAME_MAX_IMAGE_MEGAPIXELS", 25, 1, 200)
	if err != nil {
		return config{}, err
	}
	nextPollSeconds, err := getenvInt("POCKETFRAME_NEXT_POLL_SECONDS", 3600, 300, 86400)
	if err != nil {
		return config{}, err
	}
	retryAfterSeconds, err := getenvInt("POCKETFRAME_RETRY_AFTER_SECONDS", 1800, 300, 86400)
	if err != nil {
		return config{}, err
	}
	schedulePeriodSeconds, err := getenvInt("POCKETFRAME_SCHEDULE_PERIOD_SECONDS", nextPollSeconds, 300, 86400)
	if err != nil {
		return config{}, err
	}
	scheduleOffsetSeconds, err := getenvInt("POCKETFRAME_SCHEDULE_OFFSET_SECONDS", 0, 0, schedulePeriodSeconds-1)
	if err != nil {
		return config{}, err
	}
	readDelaySeconds, err := getenvInt("POCKETFRAME_READ_DELAY_SECONDS", 300, 0, 86400)
	if err != nil {
		return config{}, err
	}
	missingSlotRetrySeconds, err := getenvInt("POCKETFRAME_MISSING_SLOT_RETRY_SECONDS", 300, 300, 86400)
	if err != nil {
		return config{}, err
	}
	framePath := getenv("POCKETFRAME_FRAME_PATH", "/data/frame.jpg")

	return config{
		readToken:               readToken,
		uploadToken:             uploadToken,
		legacyUploadQuery:       legacyUploadQuery,
		framePath:               framePath,
		mode:                    mode,
		quality:                 quality,
		maxUploadBytes:          int64(maxMegabytes) * 1024 * 1024,
		maxImagePixels:          int64(maxImageMegapixels) * 1000 * 1000,
		nextPollSeconds:         nextPollSeconds,
		retryAfterSeconds:       retryAfterSeconds,
		schedulePeriodSeconds:   schedulePeriodSeconds,
		scheduleOffsetSeconds:   scheduleOffsetSeconds,
		readDelaySeconds:        readDelaySeconds,
		missingSlotRetrySeconds: missingSlotRetrySeconds,
		metadataPath:            getenv("POCKETFRAME_METADATA_PATH", framePath+".json"),
		logLevel:                strings.ToLower(getenv("POCKETFRAME_LOG_LEVEL", "info")),
	}, nil
}

func runHealthcheck() error {
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://127.0.0.1:" + getenv("PORT", "8080") + "/livez")
	if err != nil {
		return fmt.Errorf("healthcheck request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("healthcheck returned %s", response.Status)
	}
	return nil
}

func (s *server) live(writer http.ResponseWriter, request *http.Request) {
	writer.WriteHeader(http.StatusNoContent)
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getenvInt(key string, fallback, minimum, maximum int) (int, error) {
	value, err := strconv.Atoi(getenv(key, strconv.Itoa(fallback)))
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", key, minimum, maximum)
	}
	return value, nil
}

func (s *server) health(writer http.ResponseWriter, request *http.Request) {
	s.mu.RLock()
	ready := s.meta.revision != ""
	s.mu.RUnlock()
	if !ready {
		http.Error(writer, "frame is not ready", http.StatusServiceUnavailable)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *server) currentTime() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *server) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

func (s *server) schedulePeriod() int64 {
	if s.config.schedulePeriodSeconds > 0 {
		return int64(s.config.schedulePeriodSeconds)
	}
	if s.config.nextPollSeconds > 0 {
		return int64(s.config.nextPollSeconds)
	}
	return 3600
}

func (s *server) expectedSlot(at time.Time) int64 {
	period := s.schedulePeriod()
	offset := int64(s.config.scheduleOffsetSeconds)
	return ((at.Unix() - offset) / period * period) + offset
}

func (s *server) publicationState(meta metadata, now time.Time) string {
	if meta.revision == "" {
		return "empty"
	}
	expectedSlot := s.expectedSlot(now)
	if meta.publicationSlot < expectedSlot {
		return "missing_current_slot"
	}
	if meta.publicationSlot > expectedSlot {
		return "future_slot"
	}
	if now.Before(meta.readyAt) {
		return "waiting_ready_at"
	}
	return "ready"
}

func (s *server) manifestTiming(meta metadata, now time.Time) (bool, int, int) {
	expectedSlot := s.expectedSlot(now)
	ready := meta.publicationSlot == expectedSlot && !now.Before(meta.readyAt)
	if !ready {
		retry := s.config.missingSlotRetrySeconds
		if retry <= 0 {
			retry = 300
		}
		return false, retry, retry
	}

	nextReadyAt := time.Unix(expectedSlot+s.schedulePeriod()+int64(s.config.readDelaySeconds), 0)
	seconds := int(nextReadyAt.Sub(now).Seconds())
	if nextReadyAt.After(now.Add(time.Duration(seconds) * time.Second)) {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	retry := s.config.retryAfterSeconds
	if retry <= 0 {
		retry = 300
	}
	return true, seconds, retry
}

func (s *server) manifest(writer http.ResponseWriter, request *http.Request) {
	if !s.authorizedRead(writer, request) {
		return
	}

	s.mu.Lock()
	s.manifestRequests++
	now := s.currentTime()
	s.lastManifestRequestAt = now
	meta := s.meta
	manifestRequests := s.manifestRequests
	s.mu.Unlock()
	if meta.revision == "" {
		s.log().Warn("manifest unavailable", "event", "manifest_unavailable",
			"state", "empty", "manifest_requests", manifestRequests)
		http.Error(writer, "frame is not ready", http.StatusServiceUnavailable)
		return
	}

	ready, nextPoll, retryAfter := s.manifestTiming(meta, now)
	s.log().Info("manifest served", "event", "manifest_served",
		"state", s.publicationState(meta, now), "update_ready", ready,
		"publication_id", meta.publicationID, "publication_slot", meta.publicationSlot,
		"expected_publication_slot", s.expectedSlot(now),
		"next_poll_seconds", nextPoll, "retry_after_seconds", retryAfter,
		"manifest_requests", manifestRequests)
	s.writeManifest(writer, http.StatusOK, meta, now)
}

func (s *server) frame(writer http.ResponseWriter, request *http.Request) {
	if !s.authorizedRead(writer, request) {
		return
	}

	s.mu.Lock()
	s.frameRequests++
	now := s.currentTime()
	s.lastFrameRequestAt = now
	meta := s.meta
	frameRequests := s.frameRequests
	s.mu.Unlock()
	if meta.revision == "" || now.Before(meta.readyAt) {
		s.log().Warn("frame unavailable", "event", "frame_unavailable",
			"state", s.publicationState(meta, now), "publication_id", meta.publicationID,
			"publication_slot", meta.publicationSlot, "frame_requests", frameRequests)
		http.Error(writer, "frame is not ready", http.StatusServiceUnavailable)
		return
	}

	s.log().Info("frame served", "event", "frame_served",
		"publication_id", meta.publicationID, "publication_slot", meta.publicationSlot,
		"revision", meta.revision, "frame_bytes", meta.frameBytes,
		"frame_requests", frameRequests)
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(writer, request, s.config.framePath)
}

func (s *server) status(writer http.ResponseWriter, request *http.Request) {
	if !s.authorizedRead(writer, request) {
		return
	}

	s.mu.RLock()
	meta := s.meta
	manifestRequests := s.manifestRequests
	lastManifestRequestAt := s.lastManifestRequestAt
	frameRequests := s.frameRequests
	lastFrameRequestAt := s.lastFrameRequestAt
	s.mu.RUnlock()
	now := s.currentTime()
	updateReady, nextPoll, _ := s.manifestTiming(meta, now)
	startedAt := s.startedAt
	if startedAt.IsZero() {
		startedAt = now
	}
	response := statusResponse{
		Ready:                   meta.revision != "",
		ServerTime:              now.Format(time.RFC3339),
		UptimeSeconds:           int64(now.Sub(startedAt).Seconds()),
		PublicationState:        s.publicationState(meta, now),
		ExpectedPublicationSlot: s.expectedSlot(now),
		TargetWidth:             targetWidth,
		TargetHeight:            targetHeight,
		NextPollSeconds:         nextPoll,
		ManifestRequests:        manifestRequests,
		FrameRequests:           frameRequests,
	}
	response.UpdateReady = updateReady
	if !lastManifestRequestAt.IsZero() {
		response.LastManifestRequestAt = lastManifestRequestAt.Format(time.RFC3339)
	}
	if !lastFrameRequestAt.IsZero() {
		response.LastFrameRequestAt = lastFrameRequestAt.Format(time.RFC3339)
	}
	if response.Ready {
		response.Revision = meta.revision
		response.UpdatedAt = meta.publishedAt.UTC().Format(time.RFC3339)
		response.PublicationID = meta.publicationID
		response.PublicationSlot = meta.publicationSlot
		response.ScheduledAt = meta.scheduledAt.UTC().Format(time.RFC3339)
		response.ReadyAt = meta.readyAt.UTC().Format(time.RFC3339)
		response.FrameBytes = meta.frameBytes
	}

	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	if !response.Ready {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(writer).Encode(response)
	s.log().Debug("status served", "event", "status_served",
		"state", response.PublicationState, "update_ready", response.UpdateReady,
		"manifest_requests", manifestRequests, "frame_requests", frameRequests)
}

func (s *server) upload(writer http.ResponseWriter, request *http.Request) {
	if !s.authorizedUpload(writer, request) {
		return
	}
	if !isSupportedImage(request.Header.Get("Content-Type")) {
		s.logUploadRejected(request, "unsupported_media_type", nil)
		http.Error(writer, "use image/jpeg, image/png, or image/gif", http.StatusUnsupportedMediaType)
		return
	}
	if request.ContentLength > s.config.maxUploadBytes {
		s.logUploadRejected(request, "content_length_exceeded", nil)
		http.Error(writer, "image is too large", http.StatusRequestEntityTooLarge)
		return
	}
	publication, err := s.parsePublication(request)
	if err != nil {
		s.logUploadRejected(request, "invalid_publication_metadata", err)
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	s.mu.RLock()
	currentMeta := s.meta
	s.mu.RUnlock()
	if currentMeta.idempotencyKey != "" && currentMeta.idempotencyKey == publication.idempotencyKey {
		if currentMeta.publicationID != publication.id || currentMeta.publicationSlot != publication.slot {
			s.logUploadRejected(request, "idempotency_conflict", nil)
			http.Error(writer, "idempotency key conflicts with the current publication", http.StatusConflict)
			return
		}
		s.log().Info("idempotent upload reused", "event", "upload_idempotent",
			"publication_id", currentMeta.publicationID,
			"publication_slot", currentMeta.publicationSlot,
			"revision", currentMeta.revision, "frame_bytes", currentMeta.frameBytes)
		s.writeManifest(writer, http.StatusOK, currentMeta, s.currentTime())
		return
	}
	currentIsLegacy := strings.HasPrefix(currentMeta.publicationID, "legacy-")
	if currentMeta.publicationSlot > publication.slot ||
		(currentMeta.publicationSlot == publication.slot && currentMeta.publicationID != publication.id &&
			!currentIsLegacy && !publication.legacy) {
		s.logUploadRejected(request, "stale_or_conflicting_slot", nil)
		http.Error(writer, "publication slot is stale or already has another publication", http.StatusConflict)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, s.config.maxUploadBytes)
	encoded, err := io.ReadAll(request.Body)
	if err != nil {
		s.logUploadRejected(request, "body_read_failed", err)
		http.Error(writer, "image is too large or could not be read", http.StatusRequestEntityTooLarge)
		return
	}
	if len(encoded) == 0 {
		s.logUploadRejected(request, "empty_body", nil)
		http.Error(writer, "image body is required", http.StatusBadRequest)
		return
	}

	meta, err := s.publishForPublication(encoded, publication)
	if err != nil {
		s.logUploadRejected(request, "image_processing_failed", err)
		http.Error(writer, "could not process image", http.StatusBadRequest)
		return
	}
	s.log().Info("frame uploaded", "event", "upload_accepted",
		"legacy", publication.legacy, "publication_id", meta.publicationID,
		"publication_slot", meta.publicationSlot, "revision", meta.revision,
		"input_bytes", len(encoded), "frame_bytes", meta.frameBytes,
		"received_at", meta.publishedAt.Format(time.RFC3339),
		"ready_at", meta.readyAt.Format(time.RFC3339))
	s.writeManifest(writer, http.StatusCreated, meta, s.currentTime())
}

func (s *server) logUploadRejected(request *http.Request, reason string, err error) {
	attributes := []any{
		"event", "upload_rejected", "reason", reason,
		"content_type", request.Header.Get("Content-Type"),
		"content_length", request.ContentLength,
	}
	if err != nil {
		attributes = append(attributes, "error", err)
	}
	s.log().Warn("upload rejected", attributes...)
}

func (s *server) parsePublication(request *http.Request) (publication, error) {
	now := s.currentTime()
	headerNames := []string{
		"X-PocketFrame-Publication-Id",
		"X-PocketFrame-Publication-Slot",
		"X-PocketFrame-Scheduled-At",
		"X-PocketFrame-Read-Delay-Seconds",
		"Idempotency-Key",
	}
	provided := 0
	for _, name := range headerNames {
		if request.Header.Get(name) != "" {
			provided++
		}
	}
	if provided == 0 {
		slot := s.expectedSlot(now)
		id := fmt.Sprintf("legacy-%d", now.UnixNano())
		return publication{
			id: id, slot: slot, scheduledAt: time.Unix(slot, 0).UTC(),
			idempotencyKey: id, receivedAt: now, legacy: true,
		}, nil
	}
	if provided != len(headerNames) {
		return publication{}, errors.New("all PocketFrame publication headers are required")
	}

	slot, err := strconv.ParseInt(request.Header.Get("X-PocketFrame-Publication-Slot"), 10, 64)
	if err != nil || slot <= 0 {
		return publication{}, errors.New("X-PocketFrame-Publication-Slot must be a positive Unix timestamp")
	}
	period := s.schedulePeriod()
	offset := int64(s.config.scheduleOffsetSeconds)
	if (slot-offset)%period != 0 {
		return publication{}, errors.New("publication slot is not aligned to the configured UTC schedule")
	}
	expectedID := fmt.Sprintf("pocketframe-%d", slot)
	publicationID := request.Header.Get("X-PocketFrame-Publication-Id")
	idempotencyKey := request.Header.Get("Idempotency-Key")
	if publicationID != expectedID || idempotencyKey != expectedID {
		return publication{}, errors.New("publication id and idempotency key must match pocketframe-<slot>")
	}
	scheduledAt, err := time.Parse(time.RFC3339, request.Header.Get("X-PocketFrame-Scheduled-At"))
	if err != nil || !scheduledAt.Equal(time.Unix(slot, 0)) {
		return publication{}, errors.New("X-PocketFrame-Scheduled-At must equal the publication slot in RFC3339 UTC")
	}
	readDelaySeconds, err := strconv.Atoi(request.Header.Get("X-PocketFrame-Read-Delay-Seconds"))
	if err != nil || readDelaySeconds < 0 || readDelaySeconds > 86400 {
		return publication{}, errors.New("X-PocketFrame-Read-Delay-Seconds must be from 0 to 86400")
	}
	if readDelaySeconds != s.config.readDelaySeconds {
		return publication{}, errors.New("publication read delay does not match POCKETFRAME_READ_DELAY_SECONDS")
	}
	currentSlot := s.expectedSlot(now)
	if slot != currentSlot {
		return publication{}, errors.New("publication slot is outside the current scheduling window")
	}
	return publication{
		id: publicationID, slot: slot, scheduledAt: scheduledAt.UTC(),
		readDelay:      time.Duration(readDelaySeconds) * time.Second,
		idempotencyKey: idempotencyKey, receivedAt: now,
	}, nil
}

func secureTokenEqual(actual, expected string) bool {
	return hmac.Equal([]byte(actual), []byte(expected))
}

func (s *server) authorizedRead(writer http.ResponseWriter, request *http.Request) bool {
	if !secureTokenEqual(request.URL.Query().Get("token"), s.config.readToken) {
		s.log().Warn("read authorization rejected", "event", "authorization_rejected",
			"operation", "read", "path", request.URL.Path)
		http.NotFound(writer, request)
		return false
	}
	return true
}

func (s *server) authorizedUpload(writer http.ResponseWriter, request *http.Request) bool {
	fields := strings.Fields(request.Header.Get("Authorization"))
	if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") &&
		secureTokenEqual(fields[1], s.config.uploadToken) {
		return true
	}
	if s.config.legacyUploadQuery &&
		secureTokenEqual(request.URL.Query().Get("token"), s.config.uploadToken) {
		return true
	}
	s.log().Warn("upload authorization rejected", "event", "authorization_rejected",
		"operation", "upload", "path", request.URL.Path)
	http.NotFound(writer, request)
	return false
}

func (s *server) writeManifest(writer http.ResponseWriter, status int, meta metadata, now time.Time) {
	ready, nextPollSeconds, retryAfterSeconds := s.manifestTiming(meta, now)
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(status)
	fmt.Fprintf(writer, "revision=%s\npublication_id=%s\npublication_slot=%d\npublished_at=%d\nready_at=%d\nupdate_ready=%d\nnext_poll_seconds=%d\nretry_after_seconds=%d\n",
		meta.revision, meta.publicationID, meta.publicationSlot, meta.publishedAt.Unix(),
		meta.readyAt.Unix(), boolInt(ready), nextPollSeconds, retryAfterSeconds)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *server) loadExistingFrame() error {
	data, err := os.ReadFile(s.config.framePath)
	if err != nil {
		return err
	}
	info, err := os.Stat(s.config.framePath)
	if err != nil {
		return err
	}
	frameRevision := revision(data)
	meta, err := s.loadPersistedMetadata()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load frame metadata: %w", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		publishedAt := info.ModTime().UTC()
		slot := s.expectedSlot(publishedAt)
		id := fmt.Sprintf("legacy-%d", publishedAt.UnixNano())
		meta = metadata{
			revision: frameRevision, publicationID: id, publicationSlot: slot,
			scheduledAt: time.Unix(slot, 0).UTC(), publishedAt: publishedAt,
			readyAt: publishedAt, idempotencyKey: id, frameBytes: info.Size(),
		}
	} else if meta.revision != frameRevision {
		return errors.New("frame metadata revision does not match frame.jpg")
	}
	meta.frameBytes = info.Size()
	s.mu.Lock()
	s.meta = meta
	s.mu.Unlock()
	return nil
}

func (s *server) loadPersistedMetadata() (metadata, error) {
	data, err := os.ReadFile(s.config.metadataPath)
	if err != nil {
		return metadata{}, err
	}
	var persisted persistedMetadata
	if err := json.Unmarshal(data, &persisted); err != nil {
		return metadata{}, err
	}
	if persisted.Revision == "" || persisted.PublicationID == "" || persisted.PublicationSlot <= 0 ||
		persisted.PublishedAt <= 0 || persisted.ReadyAt <= 0 || persisted.IdempotencyKey == "" {
		return metadata{}, errors.New("stored frame metadata is incomplete")
	}
	return metadata{
		revision: persisted.Revision, publicationID: persisted.PublicationID,
		publicationSlot: persisted.PublicationSlot,
		scheduledAt:     time.Unix(persisted.ScheduledAt, 0).UTC(),
		publishedAt:     time.Unix(persisted.PublishedAt, 0).UTC(),
		readyAt:         time.Unix(persisted.ReadyAt, 0).UTC(),
		idempotencyKey:  persisted.IdempotencyKey, frameBytes: persisted.FrameBytes,
	}, nil
}

func (s *server) saveMetadata(meta metadata) error {
	persisted := persistedMetadata{
		Revision: meta.revision, PublicationID: meta.publicationID,
		PublicationSlot: meta.publicationSlot, ScheduledAt: meta.scheduledAt.Unix(),
		PublishedAt: meta.publishedAt.Unix(), ReadyAt: meta.readyAt.Unix(),
		IdempotencyKey: meta.idempotencyKey, FrameBytes: meta.frameBytes,
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.config.metadataPath), 0755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.config.metadataPath), ".frame-meta-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, s.config.metadataPath)
}

func revision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *server) publish(encoded []byte) (metadata, error) {
	now := s.currentTime()
	slot := s.expectedSlot(now)
	id := fmt.Sprintf("legacy-%d", now.UnixNano())
	return s.publishForPublication(encoded, publication{
		id: id, slot: slot, scheduledAt: time.Unix(slot, 0).UTC(),
		idempotencyKey: id, receivedAt: now,
	})
}

func (s *server) publishForPublication(encoded []byte, publication publication) (metadata, error) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	imageConfig, _, err := image.DecodeConfig(bytes.NewReader(encoded))
	if err != nil {
		return metadata{}, fmt.Errorf("read source dimensions: %w", err)
	}
	if imageConfig.Width <= 0 || imageConfig.Height <= 0 ||
		int64(imageConfig.Width) > s.config.maxImagePixels/int64(imageConfig.Height) {
		return metadata{}, fmt.Errorf("source image exceeds %d pixels", s.config.maxImagePixels)
	}

	source, _, err := image.Decode(bytes.NewReader(encoded))
	if err != nil {
		return metadata{}, fmt.Errorf("decode source image: %w", err)
	}

	oriented := orient(source, jpegOrientation(encoded))
	prepared := prepare(oriented, s.config.mode)
	if err := os.MkdirAll(filepath.Dir(s.config.framePath), 0755); err != nil {
		return metadata{}, fmt.Errorf("create frame directory: %w", err)
	}

	temporary, err := os.CreateTemp(filepath.Dir(s.config.framePath), ".frame-*.jpg")
	if err != nil {
		return metadata{}, fmt.Errorf("create temporary frame: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	hasher := sha256.New()
	if err := jpeg.Encode(io.MultiWriter(temporary, hasher), prepared, &jpeg.Options{Quality: s.config.quality}); err != nil {
		temporary.Close()
		return metadata{}, fmt.Errorf("encode JPEG: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return metadata{}, fmt.Errorf("close JPEG: %w", err)
	}
	newRevision := hex.EncodeToString(hasher.Sum(nil))
	s.mu.RLock()
	currentMeta := s.meta
	s.mu.RUnlock()
	frameExists := false
	if _, err := os.Stat(s.config.framePath); err == nil {
		frameExists = true
	}
	if currentMeta.revision != newRevision || !frameExists {
		if err := os.Rename(temporaryPath, s.config.framePath); err != nil {
			return metadata{}, fmt.Errorf("publish JPEG: %w", err)
		}
	}

	info, err := os.Stat(s.config.framePath)
	if err != nil {
		return metadata{}, fmt.Errorf("stat published JPEG: %w", err)
	}
	meta := metadata{
		revision: newRevision, publicationID: publication.id,
		publicationSlot: publication.slot, scheduledAt: publication.scheduledAt,
		publishedAt: publication.receivedAt, readyAt: publication.receivedAt.Add(publication.readDelay),
		idempotencyKey: publication.idempotencyKey, frameBytes: info.Size(),
	}
	if err := s.saveMetadata(meta); err != nil {
		return metadata{}, fmt.Errorf("persist frame metadata: %w", err)
	}
	s.mu.Lock()
	s.meta = meta
	s.mu.Unlock()
	return meta, nil
}

func isSupportedImage(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return contentType == "image/jpeg" || contentType == "image/png" || contentType == "image/gif"
}

func prepare(source image.Image, mode string) *image.Gray {
	target := image.NewGray(image.Rect(0, 0, targetWidth, targetHeight))
	for index := range target.Pix {
		target.Pix[index] = 255
	}

	bounds := source.Bounds()
	scaleX := float64(targetWidth) / float64(bounds.Dx())
	scaleY := float64(targetHeight) / float64(bounds.Dy())
	scale := scaleX
	if mode == "cover" && scaleY > scaleX || mode == "contain" && scaleY < scaleX {
		scale = scaleY
	}
	drawWidth := int(float64(bounds.Dx())*scale + 0.5)
	drawHeight := int(float64(bounds.Dy())*scale + 0.5)
	x := (targetWidth - drawWidth) / 2
	y := (targetHeight - drawHeight) / 2
	xdraw.CatmullRom.Scale(target, image.Rect(x, y, x+drawWidth, y+drawHeight), source, bounds, xdraw.Over, nil)
	return target
}

func orient(source image.Image, orientation int) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	at := func(x, y int) color.Color { return source.At(bounds.Min.X+x, bounds.Min.Y+y) }
	if orientation < 2 || orientation > 8 {
		return source
	}

	destination := image.NewNRGBA(image.Rect(0, 0, width, height))
	if orientation >= 5 {
		destination = image.NewNRGBA(image.Rect(0, 0, height, width))
	}
	for y := 0; y < destination.Bounds().Dy(); y++ {
		for x := 0; x < destination.Bounds().Dx(); x++ {
			switch orientation {
			case 2:
				destination.Set(x, y, at(width-1-x, y))
			case 3:
				destination.Set(x, y, at(width-1-x, height-1-y))
			case 4:
				destination.Set(x, y, at(x, height-1-y))
			case 5:
				destination.Set(x, y, at(y, x))
			case 6:
				destination.Set(x, y, at(y, height-1-x))
			case 7:
				destination.Set(x, y, at(width-1-y, height-1-x))
			case 8:
				destination.Set(x, y, at(width-1-y, x))
			}
		}
	}
	return destination
}

func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	for offset := 2; offset+4 <= len(data); {
		if data[offset] != 0xff {
			return 1
		}
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset >= len(data) {
			return 1
		}
		marker := data[offset]
		offset++
		if marker == 0xd9 || marker == 0xda {
			return 1
		}
		if offset+2 > len(data) {
			return 1
		}
		length := int(data[offset])<<8 | int(data[offset+1])
		if length < 2 || offset+length > len(data) {
			return 1
		}
		payload := data[offset+2 : offset+length]
		if marker == 0xe1 && len(payload) >= 14 && string(payload[:6]) == "Exif\x00\x00" {
			return tiffOrientation(payload[6:])
		}
		offset += length
	}
	return 1
}

func tiffOrientation(data []byte) int {
	if len(data) < 8 {
		return 1
	}
	littleEndian := string(data[:2]) == "II"
	if !littleEndian && string(data[:2]) != "MM" {
		return 1
	}
	read16 := func(offset int) int {
		if offset+2 > len(data) {
			return 0
		}
		if littleEndian {
			return int(data[offset]) | int(data[offset+1])<<8
		}
		return int(data[offset])<<8 | int(data[offset+1])
	}
	read32 := func(offset int) int {
		if offset+4 > len(data) {
			return 0
		}
		if littleEndian {
			return int(data[offset]) | int(data[offset+1])<<8 | int(data[offset+2])<<16 | int(data[offset+3])<<24
		}
		return int(data[offset])<<24 | int(data[offset+1])<<16 | int(data[offset+2])<<8 | int(data[offset+3])
	}
	if read16(2) != 42 {
		return 1
	}
	ifd := read32(4)
	if ifd < 0 || ifd+2 > len(data) {
		return 1
	}
	entries := read16(ifd)
	for index := 0; index < entries; index++ {
		offset := ifd + 2 + index*12
		if offset+12 > len(data) {
			return 1
		}
		if read16(offset) == 0x0112 && read16(offset+2) == 3 && read32(offset+4) >= 1 {
			orientation := read16(offset + 8)
			if orientation >= 1 && orientation <= 8 {
				return orientation
			}
		}
	}
	return 1
}
