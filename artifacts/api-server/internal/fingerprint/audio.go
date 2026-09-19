package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
)

func audioFingerprint(samples []byte) (hash, signature string, rms float64) {
	sum := sha256.Sum256(samples)
	hash = hex.EncodeToString(sum[:])
	if len(samples) < 2 {
		return hash, hash, 0
	}

	const bucketCount = 24
	var energy [bucketCount]float64
	var total float64
	values := make([]float64, 0, len(samples)/2)
	var previous float64
	var zeroCrossings int

	for i := 0; i+1 < len(samples); i += 2 {
		value := float64(int16(uint16(samples[i])|uint16(samples[i+1])<<8)) / 32768
		values = append(values, value)
		if len(values) > 1 && ((value < 0 && previous >= 0) || (value >= 0 && previous < 0)) {
			zeroCrossings++
		}
		previous = value
		total += value * value
	}
	sampleCount := len(values)
	if sampleCount == 0 {
		return hash, hash, 0
	}
	rms = math.Sqrt(total / float64(sampleCount))

	for bucket := 0; bucket < bucketCount; bucket++ {
		for index, value := range values {
			angle := 2 * math.Pi * float64(bucket+1) * float64(index) / float64(sampleCount)
			realPart := value * math.Cos(angle)
			imaginaryPart := value * math.Sin(angle)
			energy[bucket] += realPart*realPart + imaginaryPart*imaginaryPart
		}
		energy[bucket] = math.Sqrt(energy[bucket])
	}

	features := make([]byte, 0, bucketCount+3)
	normalizer := float64(sampleCount) * math.Max(rms, 0.000001)
	for _, value := range energy {
		quantized := int(math.Round(math.Min(255, value/normalizer*255)))
		features = append(features, byte(quantized))
	}
	features = append(features,
		byte(math.Min(255, rms*255)),
		byte(math.Min(255, float64(zeroCrossings)/float64(sampleCount)*255)),
		byte(math.Min(255, float64(sampleCount)/8000*255)),
	)
	signatureSum := sha256.Sum256(features)
	return hash, hex.EncodeToString(signatureSum[:]), rms
}
