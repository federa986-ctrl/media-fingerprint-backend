package fingerprint

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestHandlerReturnsSameFingerprintAndCleansTemporaryUpload(t *testing.T) {
	tempDir := t.TempDir()
	var source bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 7), G: uint8(y * 7), B: 120, A: 255})
		}
	}
	if err := png.Encode(&source, img); err != nil {
		t.Fatal(err)
	}

	service := NewService(Config{
		Workers:         1,
		QueueSize:       1,
		MaxUploadBytes:  1 << 20,
		DefaultInterval: time.Second,
		MaxSamples:      10,
		TempDir:         tempDir,
	})
	defer service.Close()

	fingerprint := func() Response {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/fingerprint?include_video=false&include_audio=false", bytes.NewReader(source.Bytes()))
		request.Header.Set("Content-Type", "image/png")
		recorder := httptest.NewRecorder()
		service.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
		}
		var response Response
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	first, second := fingerprint(), fingerprint()
	if first.Image == nil || second.Image == nil {
		t.Fatal("expected image fingerprints")
	}
	if first.Hashes.SHA256 != second.Hashes.SHA256 ||
		first.Image.Perceptual != second.Image.Perceptual {
		t.Fatal("same image content should produce stable fingerprints")
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary upload was not cleaned up: %d entries remain", len(entries))
	}
}
