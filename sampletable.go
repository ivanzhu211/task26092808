package mp4post

import "sort"

// TrackInfo describes one track returned by Inspect.
type TrackInfo struct {
	TrackID     uint32
	Kind        TrackKind
	Timescale   uint32
	Duration    uint64 // in track timescale units
	SampleCount uint64
	KeyCount    uint64 // number of keyframes (== SampleCount when no sync table)
}

// TrackKind classifies a track.
type TrackKind int

const (
	TrackOther TrackKind = iota
	TrackVideo
	TrackAudio
)

func (k TrackKind) String() string {
	switch k {
	case TrackVideo:
		return "video"
	case TrackAudio:
		return "audio"
	default:
		return "other"
	}
}

// sampleTable is the decoded per-track sample index (stts/stsc/stsz/stz2/
// stco/co64/stss). Memory is proportional to the index size only.
type sampleTable struct {
	timescale uint32
	duration  uint64

	// stts: decoding time to sample, plus precomputed cumulative
	// sample counts and timestamps per entry for O(log n) DTS lookup.
	stts     []sttsEntry
	sttsCum  []uint64 // samples before entry i
	sttsTime []uint64 // decode time of first sample of entry i

	// sample sizes: either one uniform size, or per-sample sizes with a
	// prefix sum (sizePrefix[i] = total bytes of samples 1..i).
	uniformSize uint32
	sizePrefix  []uint64

	// stsc: sample to chunk, plus cumulative samples per entry.
	stsc    []stscEntry
	stscCum []uint64 // samples before entry i

	chunkOffsets []int64

	// keyframes: nil means "no stss table, every sample is a keyframe".
	keyframes []uint32 // 1-based sample numbers, sorted ascending

	sampleCount uint64
}

type sttsEntry struct {
	count uint32
	delta uint32
}

type stscEntry struct {
	firstChunk      uint32
	samplesPerChunk uint32
}

// finalize precomputes the cumulative lookup arrays after parsing.
func (t *sampleTable) finalize() {
	t.sttsCum = make([]uint64, len(t.stts))
	t.sttsTime = make([]uint64, len(t.stts))
	var ns, ts uint64
	for i, e := range t.stts {
		t.sttsCum[i] = ns
		t.sttsTime[i] = ts
		ns += uint64(e.count)
		ts += uint64(e.count) * uint64(e.delta)
	}
	t.stscCum = make([]uint64, len(t.stsc))
	var cum uint64
	for i, e := range t.stsc {
		t.stscCum[i] = cum
		var chunks uint64
		if i+1 < len(t.stsc) {
			chunks = uint64(t.stsc[i+1].firstChunk - e.firstChunk)
		} else {
			if uint64(e.firstChunk) <= uint64(len(t.chunkOffsets)) {
				chunks = uint64(len(t.chunkOffsets)) - uint64(e.firstChunk) + 1
			}
		}
		cum += chunks * uint64(e.samplesPerChunk)
	}
}

// sampleSize returns the size in bytes of sample n (1-based).
func (t *sampleTable) sampleSize(n uint64) uint32 {
	if t.uniformSize != 0 {
		return t.uniformSize
	}
	if n == 0 || n > t.sampleCount || t.sizePrefix == nil {
		return 0
	}
	return uint32(t.sizePrefix[n] - t.sizePrefix[n-1])
}

// sampleDTS returns the decoding timestamp of sample n (1-based).
func (t *sampleTable) sampleDTS(n uint64) uint64 {
	if n == 0 || n > t.sampleCount || len(t.stts) == 0 {
		return 0
	}
	// find last entry with sttsCum[i] < n
	i := sort.Search(len(t.stts), func(i int) bool { return t.sttsCum[i] >= n }) - 1
	if i < 0 {
		return 0
	}
	return t.sttsTime[i] + (n-1-t.sttsCum[i])*uint64(t.stts[i].delta)
}

// isKeyframe reports whether sample n (1-based) is a keyframe.
func (t *sampleTable) isKeyframe(n uint64) bool {
	if t.keyframes == nil {
		return true
	}
	i := sort.Search(len(t.keyframes), func(i int) bool { return uint64(t.keyframes[i]) >= n })
	return i < len(t.keyframes) && uint64(t.keyframes[i]) == n
}

// locate finds the chunk (1-based) and index within the chunk (0-based)
// of sample n (1-based).
func (t *sampleTable) locate(n uint64) (chunk uint32, idxInChunk uint64, err error) {
	if n == 0 || n > t.sampleCount {
		return 0, 0, ErrSampleNotFound
	}
	if len(t.stsc) == 0 {
		return 0, 0, ErrInvalidStructure
	}
	i := sort.Search(len(t.stsc), func(i int) bool { return t.stscCum[i] >= n }) - 1
	if i < 0 {
		return 0, 0, ErrInvalidStructure
	}
	e := t.stsc[i]
	rel := n - 1 - t.stscCum[i]
	spc := uint64(e.samplesPerChunk)
	if spc == 0 {
		return 0, 0, ErrInvalidStructure
	}
	chunk = e.firstChunk + uint32(rel/spc)
	idxInChunk = rel % spc
	if chunk == 0 || int(chunk) > len(t.chunkOffsets) {
		return 0, 0, ErrInvalidStructure
	}
	return chunk, idxInChunk, nil
}

// sampleOffset returns the file offset of sample n (1-based).
func (t *sampleTable) sampleOffset(n uint64) (int64, error) {
	chunk, idx, err := t.locate(n)
	if err != nil {
		return 0, err
	}
	off := t.chunkOffsets[chunk-1]
	if t.uniformSize != 0 {
		return off + int64(idx)*int64(t.uniformSize), nil
	}
	first := n - idx // 1-based number of first sample in this chunk
	return off + int64(t.sizePrefix[first-1+idx]-t.sizePrefix[first-1]), nil
}

// lastSampleWithDTSLE returns the last sample (1-based) whose DTS is <= t,
// or 0 if none.
func (t *sampleTable) lastSampleWithDTSLE(ts uint64) uint64 {
	if len(t.stts) == 0 || t.sampleCount == 0 {
		return 0
	}
	// find last entry with sttsTime[i] <= ts
	i := sort.Search(len(t.stts), func(i int) bool { return t.sttsTime[i] > ts }) - 1
	if i < 0 {
		return 0
	}
	var n uint64
	if t.stts[i].delta == 0 {
		n = t.sttsCum[i] + uint64(t.stts[i].count)
	} else {
		n = t.sttsCum[i] + (ts-t.sttsTime[i])/uint64(t.stts[i].delta) + 1
	}
	if n > t.sampleCount {
		n = t.sampleCount
	}
	return n
}
