package mp4post

import "fmt"

// Sample is a single decoded sample read from a track.
type Sample struct {
	Data     []byte
	DTS      uint64 // decoding timestamp in track timescale units
	Keyframe bool
}

// ReadSample returns sample number n (1-based) of the given track.
func (f *File) ReadSample(trackID uint32, n uint64) (*Sample, error) {
	t, err := f.trackByIDLookup(trackID)
	if err != nil {
		return nil, err
	}
	st := t.table
	if n == 0 || n > st.sampleCount {
		return nil, ErrSampleNotFound
	}
	off, err := st.sampleOffset(n)
	if err != nil {
		return nil, err
	}
	size := st.sampleSize(n)
	if off < 0 || off+int64(size) > f.size {
		return nil, fmt.Errorf("%w: sample %d of track %d at %d size %d out of bounds",
			ErrInvalidStructure, n, trackID, off, size)
	}
	data := make([]byte, size)
	if _, err := f.f.ReadAt(data, off); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidStructure, err)
	}
	return &Sample{
		Data:     data,
		DTS:      st.sampleDTS(n),
		Keyframe: st.isKeyframe(n),
	}, nil
}

// FindKeyframe returns the 1-based number of the last keyframe whose
// decoding timestamp is <= t (in track timescale units).
func (f *File) FindKeyframe(trackID uint32, t uint64) (uint64, error) {
	tr, err := f.trackByIDLookup(trackID)
	if err != nil {
		return 0, err
	}
	st := tr.table
	if st.sampleCount == 0 {
		return 0, ErrNoKeyframe
	}
	if st.keyframes == nil {
		// No sync table: every sample is a keyframe.
		n := st.lastSampleWithDTSLE(t)
		if n == 0 {
			return 0, ErrNoKeyframe
		}
		return n, nil
	}
	// Binary search the keyframe list for the last one with DTS <= t.
	lo, hi := 0, len(st.keyframes) // invariant: answer in [lo, hi)
	for lo < hi {
		mid := (lo + hi) / 2
		if st.sampleDTS(uint64(st.keyframes[mid])) <= t {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return 0, ErrNoKeyframe
	}
	return uint64(st.keyframes[lo-1]), nil
}
