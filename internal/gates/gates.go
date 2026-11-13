// Package gates verifies converted outputs before replacing originals.
package gates

// DurationKept reports whether the new duration keeps at least minFraction
// of the source duration (a damaged source losing whole sections fails).
func DurationKept(newDur, srcDur, minFraction float64) bool {
	return newDur >= srcDur*minFraction
}

// Shrunk reports whether the new size is strictly smaller than the source
// (a re-encode must never grow the file).
func Shrunk(newSize, srcSize int64) bool {
	return newSize < srcSize
}

// Remux constants for the index overhead a lossless remux may add: a seek
// index entry or container block per frame in the worst case (every frame
// keyframed in Matroska, every frame a sample-table entry in MP4, every
// audio frame its own block) plus a fixed allowance for tags, headers and
// metadata.
const (
	indexBytesPerFrame = 32
	indexAllowance     = 64 << 10
)

// IndexBudget returns the size overhead a lossless -c copy remux may add
// over the source for a file of frames frames (video and audio frame
// count; 0 when unknown, leaving just the fixed allowance).
func IndexBudget(frames int64) int64 {
	if frames <= 0 {
		return indexAllowance
	}
	return indexAllowance + frames*indexBytesPerFrame
}

// remuxBudgetFloor is the minimum overhead a removal-only remux may add:
// 1% of the source. Real muxer-layout growth (audio lacing differences,
// cluster and index granularity, tags) stays around 0.05% of the file, so
// the floor can never false-fail a tiny stream removal, while genuine
// remux disasters still fail.
const remuxBudgetFloorPercent = 100

// RemuxBudget returns the size overhead a lossless -c copy remux may add
// over the source for a file of frames frames: the per-frame index model
// raised to at least 1% of the source. The floor keeps layout differences
// between the original muxer and ffmpeg (audio lacing, cluster and cue
// granularity, tags) that the frame model misses - and models badly on
// files with unknown fps - from ever rejecting a legit removal.
func RemuxBudget(srcSize, frames int64) int64 {
	if floor := srcSize / remuxBudgetFloorPercent; floor > IndexBudget(frames) {
		return floor
	}
	return IndexBudget(frames)
}

// WithinBudget reports whether newSize stays within srcSize plus budget.
// Used for removal-only remuxes, where dropping a tiny stream can lose to
// the re-written seek index; correctness there is guaranteed by the
// duration and stream-count gates.
func WithinBudget(newSize, srcSize, budget int64) bool {
	return newSize <= srcSize+budget
}
