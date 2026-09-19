package fingerprint

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Service struct {
	config     Config
	queue      chan job
	slots      chan struct{}
	tempBudget *byteBudget
	stop       chan struct{}
	wg         sync.WaitGroup
	nextID     atomic.Uint64
}

type job struct {
	ctx       context.Context
	path      string
	options   Options
	queuedAt  time.Time
	tempBytes int64
	result    chan jobResult
}

type jobResult struct {
	response *Response
	err      error
}

func NewService(config Config) *Service {
	if config.Workers <= 0 {
		config.Workers = min(4, max(1, runtime.NumCPU()/2))
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 16
	}
	if config.MaxUploadBytes <= 0 {
		config.MaxUploadBytes = 512 << 20
	}
	if config.MaxTempBytes <= 0 {
		config.MaxTempBytes = 2 << 30
	}
	if config.DefaultInterval <= 0 {
		config.DefaultInterval = time.Second
	}
	if config.MaxSamples <= 0 {
		config.MaxSamples = 3600
	}
	service := &Service{
		config:     config,
		queue:      make(chan job, config.QueueSize),
		slots:      make(chan struct{}, config.Workers+config.QueueSize),
		tempBudget: newByteBudget(config.MaxTempBytes),
		stop:       make(chan struct{}),
	}
	for i := 0; i < config.Workers; i++ {
		service.wg.Add(1)
		go service.worker()
	}
	return service
}

func (s *Service) Close() {
	close(s.stop)
	s.wg.Wait()
	for {
		select {
		case current := <-s.queue:
			s.cleanupJob(current)
			select {
			case current.result <- jobResult{err: errors.New("fingerprint service is shutting down")}:
			default:
			}
		default:
			return
		}
	}
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/healthz", s.health)
	mux.HandleFunc("/api/v1/fingerprint", s.handleFingerprint)
	return withCORS(mux)
}

func (s *Service) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stop:
			return
		case current := <-s.queue:
			started := time.Now()
			response, err := analyze(current.ctx, current.path, current.options)
			if response != nil {
				response.QueueWaitMs = started.Sub(current.queuedAt).Milliseconds()
			}
			s.cleanupJob(current)
			select {
			case current.result <- jobResult{response: response, err: err}:
			case <-current.ctx.Done():
			}
		}
	}
}

func (s *Service) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":              "ok",
		"workers":             s.config.Workers,
		"queue_capacity":      cap(s.queue),
		"queue_depth":         len(s.queue),
		"in_flight_capacity":  cap(s.slots),
		"in_flight":           len(s.slots),
		"temp_bytes_in_use":   s.tempBudget.Used(),
		"max_temp_bytes":      s.config.MaxTempBytes,
		"max_upload_bytes":    s.config.MaxUploadBytes,
		"default_interval_ms": s.config.DefaultInterval.Milliseconds(),
	})
}

func (s *Service) handleFingerprint(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use POST for fingerprinting.")
		return
	}
	if r.ContentLength > s.config.MaxUploadBytes && r.ContentLength > 0 {
		writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "The media file exceeds the configured upload limit.")
		return
	}

	requestID := s.requestID()
	if !s.acquireSlot() {
		writeError(w, http.StatusTooManyRequests, "capacity_full", "Fingerprint capacity is full; retry with backoff.")
		return
	}
	slotHeld := true
	defer func() {
		if slotHeld {
			s.releaseSlot()
		}
	}()

	tempFile, options, tempBytes, err := s.saveUpload(r)
	if err != nil {
		writeError(w, statusForError(err), "invalid_upload", err.Error())
		return
	}

	result := make(chan jobResult, 1)
	current := job{
		ctx:       r.Context(),
		path:      tempFile,
		options:   options,
		queuedAt:  time.Now(),
		tempBytes: tempBytes,
		result:    result,
	}
	select {
	case s.queue <- current:
	case <-r.Context().Done():
		s.cleanupJob(current)
		slotHeld = false
		return
	}

	select {
	case completed := <-result:
		slotHeld = false
		if completed.err != nil {
			writeError(w, statusForError(completed.err), "fingerprint_failed", completed.err.Error())
			return
		}
		completed.response.RequestID = requestID
		writeJSON(w, http.StatusOK, completed.response)
	case <-r.Context().Done():
		slotHeld = false
		return
	}
}

func (s *Service) saveUpload(r *http.Request) (string, Options, int64, error) {
	options := Options{
		Interval:    s.config.DefaultInterval,
		MaxSamples:  s.config.MaxSamples,
		Video:       true,
		Audio:       true,
		Image:       true,
		ContentType: r.Header.Get("Content-Type"),
		Filename:    r.Header.Get("X-Filename"),
	}
	if value := r.URL.Query().Get("interval_ms"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 100 || parsed > 60000 {
			return "", options, 0, errors.New("interval_ms must be an integer between 100 and 60000")
		}
		options.Interval = time.Duration(parsed) * time.Millisecond
	}
	if value := r.URL.Query().Get("max_samples"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > s.config.MaxSamples {
			return "", options, 0, fmt.Errorf("max_samples must be between 1 and %d", s.config.MaxSamples)
		}
		options.MaxSamples = parsed
	}
	options.Video = queryBool(r, "include_video", true)
	options.Audio = queryBool(r, "include_audio", true)
	options.Image = queryBool(r, "include_image", true)

	tempFile, err := os.CreateTemp(s.config.TempDir, "media-fingerprint-*")
	if err != nil {
		return "", options, 0, fmt.Errorf("create temporary upload: %w", err)
	}
	path := tempFile.Name()
	cleanup := func(cause error) (string, Options, int64, error) {
		_ = tempFile.Close()
		_ = os.Remove(path)
		return "", options, 0, cause
	}

	reader := io.Reader(io.LimitReader(r.Body, s.config.MaxUploadBytes+1))
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		mediaFound := false
		mediaType, params, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if parseErr != nil || mediaType != "multipart/form-data" {
			return cleanup(errors.New("invalid multipart content type"))
		}
		multipartReader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, nextErr := multipartReader.NextPart()
			if errors.Is(nextErr, io.EOF) {
				break
			}
			if nextErr != nil {
				return cleanup(fmt.Errorf("read multipart upload: %w", nextErr))
			}
			if part.FormName() != "media" && part.FormName() != "file" {
				continue
			}
			mediaFound = true
			if options.Filename == "" {
				options.Filename = part.FileName()
			}
			if options.ContentType == "" || strings.HasPrefix(options.ContentType, "multipart/") {
				options.ContentType = part.Header.Get("Content-Type")
			}
			reader = io.LimitReader(part, s.config.MaxUploadBytes+1)
			break
		}
		if !mediaFound {
			return cleanup(errors.New("multipart upload must include a media or file field"))
		}
	}

	budgetWriter := &budgetWriter{destination: tempFile, budget: s.tempBudget}
	if _, err := io.Copy(budgetWriter, reader); err != nil {
		s.tempBudget.Release(budgetWriter.reserved)
		return cleanup(fmt.Errorf("write temporary upload: %w", err))
	}
	if info, statErr := tempFile.Stat(); statErr == nil && info.Size() > s.config.MaxUploadBytes {
		s.tempBudget.Release(budgetWriter.reserved)
		return cleanup(errors.New("upload is too large"))
	}
	if err := tempFile.Close(); err != nil {
		s.tempBudget.Release(budgetWriter.reserved)
		_ = os.Remove(path)
		return "", options, 0, fmt.Errorf("close temporary upload: %w", err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		s.tempBudget.Release(budgetWriter.reserved)
		_ = os.Remove(path)
		return "", options, 0, errors.New("upload is empty")
	}
	return path, options, budgetWriter.reserved, nil
}

func (s *Service) acquireSlot() bool {
	select {
	case s.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Service) releaseSlot() {
	<-s.slots
}

func (s *Service) cleanupJob(current job) {
	_ = os.Remove(current.path)
	s.tempBudget.Release(current.tempBytes)
	s.releaseSlot()
}

func analyze(ctx context.Context, path string, options Options) (*Response, error) {
	started := time.Now()
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read temporary upload: %w", err)
	}
	mediaHash, err := hashFile(path)
	if err != nil {
		return nil, err
	}
	probe, err := probeMedia(ctx, path)
	if err != nil {
		return nil, err
	}
	var duration float64
	if probe.Format.Duration != "" {
		duration, _ = strconv.ParseFloat(probe.Format.Duration, 64)
	}
	hasVideo, hasAudio := false, false
	for _, stream := range probe.Streams {
		hasVideo = hasVideo || stream.CodecType == "video"
		hasAudio = hasAudio || stream.CodecType == "audio"
	}
	kind := mediaKind(options.ContentType, options.Filename, hasVideo, hasAudio)
	response := &Response{
		MediaKind:       kind,
		ContentType:     options.ContentType,
		SizeBytes:       info.Size(),
		Hashes:          mediaHash,
		DurationSeconds: duration,
	}

	if kind == "image" && options.Image {
		image, imageErr := fingerprintImage(ctx, path)
		if imageErr != nil {
			return nil, imageErr
		}
		response.Image = image
	}

	var video *VideoFingerprint
	var audio *AudioFingerprint
	var videoErr, audioErr error
	var workers sync.WaitGroup
	sampleLimit := options.MaxSamples
	if duration > 0 {
		sampleLimit = min(sampleLimit, max(1, int((duration/options.Interval.Seconds())+0.999999)))
	}
	if hasVideo && options.Video && kind != "image" {
		workers.Add(1)
		go func() {
			defer workers.Done()
			video, videoErr = fingerprintVideo(ctx, path, options, sampleLimit)
		}()
	}
	if hasAudio && options.Audio {
		workers.Add(1)
		go func() {
			defer workers.Done()
			audio, audioErr = fingerprintAudio(ctx, path, options, sampleLimit)
		}()
	}
	workers.Wait()
	if videoErr != nil {
		return nil, videoErr
	}
	if audioErr != nil {
		return nil, audioErr
	}
	response.Video = video
	response.Audio = audio
	response.ProcessingMs = time.Since(started).Milliseconds()
	return response, nil
}

func probeMedia(ctx context.Context, path string) (probeResult, error) {
	var result probeResult
	output, err := commandOutput(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type", "-of", "json", path)
	if err != nil {
		return result, fmt.Errorf("media probe failed: %w", err)
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return result, fmt.Errorf("parse media probe: %w", err)
	}
	return result, nil
}

func fingerprintImage(ctx context.Context, path string) (*ImageFingerprint, error) {
	rgb, err := commandOutput(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-i", path, "-frames:v", "1", "-vf", "scale=32:32:flags=area,format=rgb24", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
	if err != nil {
		return nil, fmt.Errorf("image decode failed: %w", err)
	}
	if len(rgb) < imageSide*imageSide*3 {
		return nil, errors.New("image decoder returned an incomplete frame")
	}
	sum := sha256.Sum256(rgb)
	md5sum := md5.Sum(rgb)
	return &ImageFingerprint{
		SHA256:     hex.EncodeToString(sum[:]),
		MD5:        hex.EncodeToString(md5sum[:]),
		Perceptual: perceptualHashRGB(rgb),
	}, nil
}

func fingerprintVideo(ctx context.Context, path string, options Options, sampleLimit int) (*VideoFingerprint, error) {
	intervalSeconds := options.Interval.Seconds()
	filter := fmt.Sprintf("fps=1/%s,scale=32:32:flags=area,format=rgb24", strconv.FormatFloat(intervalSeconds, 'f', 4, 64))
	output, err := commandOutput(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-i", path, "-an", "-vf", filter, "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
	if err != nil {
		return nil, fmt.Errorf("video fingerprint extraction failed: %w", err)
	}
	frameSize := imageSide * imageSide * 3
	result := &VideoFingerprint{IntervalMs: int(options.Interval.Milliseconds()), Samples: make([]VideoSample, 0)}
	for index, offset := 0, 0; offset+frameSize <= len(output) && index < sampleLimit; index, offset = index+1, offset+frameSize {
		frame := output[offset : offset+frameSize]
		sum := sha256.Sum256(frame)
		result.Samples = append(result.Samples, VideoSample{
			TimestampMs: int64(index) * options.Interval.Milliseconds(),
			SHA256:      hex.EncodeToString(sum[:]),
			Perceptual:  perceptualHashRGB(frame),
		})
	}
	return result, nil
}

func fingerprintAudio(ctx context.Context, path string, options Options, sampleLimit int) (*AudioFingerprint, error) {
	const sampleRate = 8000
	windowSamples := max(1, int(float64(sampleRate)*options.Interval.Seconds()))
	windowBytes := windowSamples * 2
	command := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-i", path, "-vn", "-ac", "1", "-ar", strconv.Itoa(sampleRate), "-f", "s16le", "-")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("audio fingerprint extraction failed: %w", err)
	}
	result := &AudioFingerprint{IntervalMs: int(options.Interval.Milliseconds()), Samples: make([]AudioSample, 0)}
	for index, offset := 0, 0; offset < len(output) && index < sampleLimit; index, offset = index+1, offset+windowBytes {
		end := min(len(output), offset+windowBytes)
		if end-offset < 2 {
			break
		}
		hash, signature, rms := audioFingerprint(output[offset:end])
		result.Samples = append(result.Samples, AudioSample{
			TimestampMs: int64(index) * options.Interval.Milliseconds(),
			SHA256:      hash,
			Signature:   signature,
			RMS:         rms,
		})
	}
	return result, nil
}

func hashFile(path string) (Hashes, error) {
	file, err := os.Open(path)
	if err != nil {
		return Hashes{}, fmt.Errorf("open upload for hashing: %w", err)
	}
	defer file.Close()
	sha := sha256.New()
	md5hash := md5.New()
	if _, err := io.Copy(io.MultiWriter(sha, md5hash), file); err != nil {
		return Hashes{}, fmt.Errorf("hash upload: %w", err)
	}
	return Hashes{
		SHA256: hex.EncodeToString(sha.Sum(nil)),
		MD5:    hex.EncodeToString(md5hash.Sum(nil)),
	}, nil
}

func commandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && len(exitError.Stderr) > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitError.Stderr)))
		}
		return nil, err
	}
	return output, nil
}

func mediaKind(contentType, filename string, hasVideo, hasAudio bool) string {
	if strings.HasPrefix(strings.ToLower(contentType), "image/") ||
		strings.HasPrefix(strings.ToLower(filepath.Ext(filename)), ".jpg") ||
		strings.HasPrefix(strings.ToLower(filepath.Ext(filename)), ".jpeg") ||
		strings.HasPrefix(strings.ToLower(filepath.Ext(filename)), ".png") ||
		strings.HasPrefix(strings.ToLower(filepath.Ext(filename)), ".webp") {
		return "image"
	}
	if hasVideo {
		return "video"
	}
	if hasAudio {
		return "audio"
	}
	return "unknown"
}

func queryBool(r *http.Request, key string, fallback bool) bool {
	value := r.URL.Query().Get(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

func (s *Service) requestID() string {
	return fmt.Sprintf("fp_%d_%d", time.Now().UnixMilli(), s.nextID.Add(1))
}

func statusForError(err error) int {
	switch {
	case strings.Contains(err.Error(), "too large"):
		return http.StatusRequestEntityTooLarge
	case strings.Contains(err.Error(), "queue"),
		strings.Contains(err.Error(), "capacity"),
		strings.Contains(err.Error(), "budget"):
		return http.StatusTooManyRequests
	case strings.Contains(err.Error(), "probe failed"), strings.Contains(err.Error(), "decoder failed"):
		return http.StatusUnsupportedMediaType
	default:
		return http.StatusBadRequest
	}
}

type byteBudget struct {
	mu    sync.Mutex
	used  int64
	limit int64
}

func newByteBudget(limit int64) *byteBudget {
	return &byteBudget{limit: limit}
}

func (b *byteBudget) TryReserve(amount int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if amount < 0 || b.used > b.limit-amount {
		return false
	}
	b.used += amount
	return true
}

func (b *byteBudget) Release(amount int64) {
	b.mu.Lock()
	b.used = max(0, b.used-amount)
	b.mu.Unlock()
}

func (b *byteBudget) Used() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

type budgetWriter struct {
	destination io.Writer
	budget      *byteBudget
	reserved    int64
}

func (w *budgetWriter) Write(p []byte) (int, error) {
	if !w.budget.TryReserve(int64(len(p))) {
		return 0, errors.New("temporary storage budget exceeded")
	}
	written, err := w.destination.Write(p)
	if written < len(p) {
		w.budget.Release(int64(len(p) - written))
	}
	w.reserved += int64(written)
	return written, err
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Filename")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		next.ServeHTTP(w, r)
	})
}
