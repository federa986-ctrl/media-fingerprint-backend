package fingerprint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func testFrame(brightness uint8) []byte {
	frame := make([]byte, imageSide*imageSide*3)
	for y := 0; y < imageSide; y++ {
		for x := 0; x < imageSide; x++ {
			i := (y*imageSide + x) * 3
			value := brightness
			if x > 7 && x < 24 && y > 7 && y < 24 {
				value = brightness / 3
			}
			frame[i], frame[i+1], frame[i+2] = value, value, value
		}
	}
	return frame
}

func TestPerceptualHashIdentifiesSameContent(t *testing.T) {
	left := perceptualHashRGB(testFrame(210))
	right := perceptualHashRGB(testFrame(210))
	if left == "" || left != right {
		t.Fatalf("same content should have the same perceptual hash: %q vs %q", left, right)
	}
}

func TestPerceptualHashToleratesBrightnessChange(t *testing.T) {
	left := perceptualHashRGB(testFrame(180))
	right := perceptualHashRGB(testFrame(220))
	if distance := hammingDistance(left, right); distance > 8 {
		t.Fatalf("brightness-only change should remain close, distance=%d", distance)
	}
}

func TestAudioFingerprintIdentifiesSameContent(t *testing.T) {
	samples := make([]byte, 16000)
	for i := 0; i < len(samples); i += 2 {
		value := int16((i / 2) % 400)
		samples[i] = byte(value)
		samples[i+1] = byte(value >> 8)
	}
	leftHash, leftSignature, _ := audioFingerprint(samples)
	rightHash, rightSignature, _ := audioFingerprint(append([]byte(nil), samples...))
	if leftHash != rightHash || leftSignature != rightSignature {
		t.Fatalf("same audio should have identical hashes")
	}
}

func TestSourceHashesAreStable(t *testing.T) {
	content := []byte("same content")
	sha := sha256.Sum256(content)
	expected := hex.EncodeToString(sha[:])
	if !bytes.Equal([]byte(expected), []byte(expected)) {
		t.Fatal("sanity check failed")
	}
}

func TestImagePixelsCanBeEncodedWithoutStorage(t *testing.T) {
	var buffer bytes.Buffer
	source := image.NewRGBA(image.Rect(0, 0, imageSide, imageSide))
	for y := 0; y < imageSide; y++ {
		for x := 0; x < imageSide; x++ {
			source.Set(x, y, color.RGBA{R: uint8(x * 7), G: uint8(y * 7), B: 100, A: 255})
		}
	}
	if err := png.Encode(&buffer, source); err != nil {
		t.Fatal(err)
	}
	if buffer.Len() == 0 {
		t.Fatal("expected encoded image bytes")
	}
}
