package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	xdraw "golang.org/x/image/draw"
)

const (
	targetWidth  = 1404
	targetHeight = 1872
)

type config struct {
	token             string
	framePath         string
	mode              string
	quality           int
	maxUploadBytes    int64
	nextPollSeconds   int
	retryAfterSeconds int
}

type metadata struct {
	revision string
}

type server struct {
	config    config
	mu        sync.RWMutex
	publishMu sync.Mutex
	meta      metadata
}

func main() {
	config, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	server := &server{config: config}
	if err := server.loadExistingFrame(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatalf("load existing frame: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.health)
	mux.HandleFunc("GET /manifest", server.manifest)
	mux.HandleFunc("GET /frame.jpg", server.frame)
	mux.HandleFunc("POST /api/frame", server.upload)

	address := ":" + getenv("PORT", "8080")
	log.Printf("PocketFrame server listening on %s", address)
	if err := http.ListenAndServe(address, mux); err != nil {
		log.Fatal(err)
	}
}

func loadConfig() (config, error) {
	token := os.Getenv("POCKETFRAME_TOKEN")
	if len(token) < 16 {
		return config{}, errors.New("POCKETFRAME_TOKEN must contain at least 16 characters")
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
	nextPollSeconds, err := getenvInt("POCKETFRAME_NEXT_POLL_SECONDS", 3600, 300, 86400)
	if err != nil {
		return config{}, err
	}
	retryAfterSeconds, err := getenvInt("POCKETFRAME_RETRY_AFTER_SECONDS", 1800, 300, 86400)
	if err != nil {
		return config{}, err
	}

	return config{
		token:             token,
		framePath:         getenv("POCKETFRAME_FRAME_PATH", "/data/frame.jpg"),
		mode:              mode,
		quality:           quality,
		maxUploadBytes:    int64(maxMegabytes) * 1024 * 1024,
		nextPollSeconds:   nextPollSeconds,
		retryAfterSeconds: retryAfterSeconds,
	}, nil
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

func (s *server) manifest(writer http.ResponseWriter, request *http.Request) {
	if !s.authorized(writer, request) {
		return
	}

	s.mu.RLock()
	meta := s.meta
	s.mu.RUnlock()
	if meta.revision == "" {
		http.Error(writer, "frame is not ready", http.StatusServiceUnavailable)
		return
	}

	s.writeManifest(writer, http.StatusOK, meta)
}

func (s *server) frame(writer http.ResponseWriter, request *http.Request) {
	if !s.authorized(writer, request) {
		return
	}

	s.mu.RLock()
	ready := s.meta.revision != ""
	s.mu.RUnlock()
	if !ready {
		http.Error(writer, "frame is not ready", http.StatusServiceUnavailable)
		return
	}

	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(writer, request, s.config.framePath)
}

func (s *server) upload(writer http.ResponseWriter, request *http.Request) {
	if !s.authorized(writer, request) {
		return
	}
	if !isSupportedImage(request.Header.Get("Content-Type")) {
		http.Error(writer, "use image/jpeg, image/png, or image/gif", http.StatusUnsupportedMediaType)
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, s.config.maxUploadBytes)
	encoded, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, "image is too large or could not be read", http.StatusRequestEntityTooLarge)
		return
	}
	if len(encoded) == 0 {
		http.Error(writer, "image body is required", http.StatusBadRequest)
		return
	}

	meta, err := s.publish(encoded)
	if err != nil {
		log.Printf("reject upload: %v", err)
		http.Error(writer, "could not process image", http.StatusBadRequest)
		return
	}
	s.writeManifest(writer, http.StatusCreated, meta)
}

func (s *server) authorized(writer http.ResponseWriter, request *http.Request) bool {
	if request.URL.Query().Get("token") != s.config.token {
		http.NotFound(writer, request)
		return false
	}
	return true
}

func (s *server) writeManifest(writer http.ResponseWriter, status int, meta metadata) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(status)
	fmt.Fprintf(writer, "revision=%s\nnext_poll_seconds=%d\nretry_after_seconds=%d\n",
		meta.revision, s.config.nextPollSeconds, s.config.retryAfterSeconds)
}

func (s *server) loadExistingFrame() error {
	data, err := os.ReadFile(s.config.framePath)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.meta = metadata{revision: revision(data)}
	s.mu.Unlock()
	return nil
}

func (s *server) publish(encoded []byte) (metadata, error) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

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
	if err := os.Rename(temporaryPath, s.config.framePath); err != nil {
		return metadata{}, fmt.Errorf("publish JPEG: %w", err)
	}

	meta := metadata{revision: hex.EncodeToString(hasher.Sum(nil))}
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
