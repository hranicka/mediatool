// Package gates verifies converted outputs before replacing originals.
package gates

// DurationKept reports whether the new duration keeps at least minFraction
// of the source duration (a damaged source losing whole sections fails).
func DurationKept(newDur, srcDur, minFraction float64) bool {
	return newDur >= srcDur*minFraction
}

// Shrunk reports whether the new size is strictly smaller than the source
// (a re-encode or a removal-only remux must never grow the file).
func Shrunk(newSize, srcSize int64) bool {
	return newSize < srcSize
}
