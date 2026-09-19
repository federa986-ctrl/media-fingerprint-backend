package fingerprint

import "time"

type Config struct {
	Workers         int
	QueueSize       int
	MaxUploadBytes  int64
	MaxTempBytes    int64
	DefaultInterval time.Duration
	MaxSamples      int
	TempDir         string
}

type Options struct {
	Interval    time.Duration
	MaxSamples  int
	Video       bool
	Audio       bool
	Image       bool
	ContentType string
	Filename    string
}

type Response struct {
	RequestID       string            `json:"request_id"`
	MediaKind       string            `json:"media_kind"`
	ContentType     string            `json:"content_type"`
	SizeBytes       int64             `json:"size_bytes"`
	Hashes          Hashes            `json:"hashes"`
	DurationSeconds float64           `json:"duration_seconds,omitempty"`
	QueueWaitMs     int64             `json:"queue_wait_ms"`
	ProcessingMs    int64             `json:"processing_ms"`
	Video           *VideoFingerprint `json:"video,omitempty"`
	Audio           *AudioFingerprint `json:"audio,omitempty"`
	Image           *ImageFingerprint `json:"image,omitempty"`
}

type Hashes struct {
	SHA256 string `json:"sha256"`
	MD5    string `json:"md5"`
}

type VideoFingerprint struct {
	IntervalMs int           `json:"interval_ms"`
	Samples    []VideoSample `json:"samples"`
}

type VideoSample struct {
	TimestampMs int64  `json:"timestamp_ms"`
	SHA256      string `json:"sha256"`
	Perceptual  string `json:"perceptual_hash"`
}

type AudioFingerprint struct {
	IntervalMs int           `json:"interval_ms"`
	Samples    []AudioSample `json:"samples"`
}

type AudioSample struct {
	TimestampMs int64   `json:"timestamp_ms"`
	SHA256      string  `json:"sha256"`
	Signature   string  `json:"signature"`
	RMS         float64 `json:"rms"`
}

type ImageFingerprint struct {
	SHA256     string `json:"sha256"`
	MD5        string `json:"md5"`
	Perceptual string `json:"perceptual_hash"`
}

type probeResult struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecType string `json:"codec_type"`
	} `json:"streams"`
}
