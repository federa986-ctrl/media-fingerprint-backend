package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
)

const imageSide = 32

func perceptualHashRGB(rgb []byte) string {
	if len(rgb) < imageSide*imageSide*3 {
		return ""
	}

	var values [imageSide][imageSide]float64
	var total float64
	for y := 0; y < imageSide; y++ {
		for x := 0; x < imageSide; x++ {
			i := (y*imageSide + x) * 3
			values[y][x] = 0.299*float64(rgb[i]) +
				0.587*float64(rgb[i+1]) +
				0.114*float64(rgb[i+2])
			total += values[y][x]
		}
	}
	mean := total / (imageSide * imageSide)
	var variance float64
	for y := 0; y < imageSide; y++ {
		for x := 0; x < imageSide; x++ {
			delta := values[y][x] - mean
			variance += delta * delta
		}
	}
	deviation := math.Sqrt(variance / (imageSide * imageSide))
	if deviation < 0.000001 {
		deviation = 1
	}
	for y := 0; y < imageSide; y++ {
		for x := 0; x < imageSide; x++ {
			values[y][x] = (values[y][x] - mean) / deviation
		}
	}

	var coefficients [64]float64
	for u := 0; u < 8; u++ {
		for v := 0; v < 8; v++ {
			var sum float64
			for y := 0; y < imageSide; y++ {
				for x := 0; x < imageSide; x++ {
					sum += values[y][x] *
						math.Cos((float64(2*x+1)*float64(u)*math.Pi)/(2*imageSide)) *
						math.Cos((float64(2*y+1)*float64(v)*math.Pi)/(2*imageSide))
				}
			}
			au, av := 1.0, 1.0
			if u == 0 {
				au = 1 / math.Sqrt2
			}
			if v == 0 {
				av = 1 / math.Sqrt2
			}
			coefficients[u*8+v] = 0.25 * au * av * sum
		}
	}

	var sorted [63]float64
	copy(sorted[:], coefficients[1:])
	for i := 1; i < len(sorted); i++ {
		key := sorted[i]
		j := i - 1
		for j >= 0 && sorted[j] > key {
			sorted[j+1] = sorted[j]
			j--
		}
		sorted[j+1] = key
	}
	median := sorted[len(sorted)/2]

	var hash uint64
	for i, coefficient := range coefficients[1:] {
		if coefficient > median {
			hash |= uint64(1) << uint(i)
		}
	}
	return hex.EncodeToString([]byte{
		byte(hash >> 56), byte(hash >> 48), byte(hash >> 40), byte(hash >> 32),
		byte(hash >> 24), byte(hash >> 16), byte(hash >> 8), byte(hash),
	})
}

func imageContentHash(rgb []byte) string {
	sum := sha256.Sum256(rgb)
	return hex.EncodeToString(sum[:])
}

func hammingDistance(left, right string) int {
	if len(left) != len(right) {
		return imageSide * 2
	}
	var distance int
	for i := range left {
		distance += bitsSet(hexNibble(left[i]) ^ hexNibble(right[i]))
	}
	return distance
}

func hexNibble(value byte) byte {
	switch {
	case value >= '0' && value <= '9':
		return value - '0'
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10
	default:
		return 0
	}
}

func bitsSet(value byte) int {
	count := 0
	for value != 0 {
		value &= value - 1
		count++
	}
	return count
}
