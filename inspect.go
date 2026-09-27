package mp4post

import (
	"fmt"
	"os"
)

// File is an opened, parsed MP4 file. Only the index is held in memory;
// media data is read on demand. Close it when done.
type File struct {
	f         *os.File
	size      int64
	boxes     []boxHeader // all top-level boxes, in file order
	moovIdx   int         // index into boxes of the moov box
	firstMdat int         // index into boxes of the first mdat, -1 if none
	tracks    []*track
	trackByID map[uint32]*track
}

// Open opens and parses the MP4 at path, validating its structure.
// Only the index is read; media data is never touched.
func Open(path string) (*File, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := fh.Stat()
	if err != nil {
		fh.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		fh.Close()
		return nil, fmt.Errorf("%w: not a regular file", ErrInvalidStructure)
	}
	size := fi.Size()
	boxes, err := scanTopLevel(fh, size)
	if err != nil {
		fh.Close()
		return nil, err
	}
	f := &File{
		f:         fh,
		size:      size,
		boxes:     boxes,
		moovIdx:   -1,
		firstMdat: -1,
		trackByID: map[uint32]*track{},
	}
	for i, b := range boxes {
		switch string(b.typ[:]) {
		case "moov":
			if f.moovIdx >= 0 {
				fh.Close()
				return nil, fmt.Errorf("%w: multiple moov boxes", ErrInvalidStructure)
			}
			f.moovIdx = i
		case "mdat":
			if f.firstMdat < 0 {
				f.firstMdat = i
			}
		case "moof":
			fh.Close()
			return nil, ErrFragmented
		case "cmov":
			fh.Close()
			return nil, ErrCompressedMoov
		}
	}
	if f.moovIdx < 0 {
		fh.Close()
		return nil, ErrNoMoov
	}
	moovPayload, err := readPayload(fh, boxes[f.moovIdx])
	if err != nil {
		fh.Close()
		return nil, err
	}
	f.tracks, err = parseMoov(moovPayload)
	if err != nil {
		fh.Close()
		return nil, err
	}
	for _, t := range f.tracks {
		if _, dup := f.trackByID[t.id]; dup {
			fh.Close()
			return nil, fmt.Errorf("%w: duplicate track id %d", ErrInvalidStructure, t.id)
		}
		f.trackByID[t.id] = t
	}
	return f, nil
}

// Report is the result of Inspect.
type Report struct {
	Tracks              []TrackInfo
	MoovBeforeFirstMdat bool
}

// Inspect opens path, reports per-track info and whether the index
// already precedes the first media data box, and closes the file.
func Inspect(path string) (*Report, error) {
	f, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return &Report{Tracks: f.Tracks(), MoovBeforeFirstMdat: f.MoovBeforeFirstMdat()}, nil
}

// Close releases the underlying file handle.
func (f *File) Close() error {
	return f.f.Close()
}

// Tracks returns one TrackInfo per track, in file order.
func (f *File) Tracks() []TrackInfo {
	out := make([]TrackInfo, len(f.tracks))
	for i, t := range f.tracks {
		keyCount := t.table.sampleCount
		if t.table.keyframes != nil {
			keyCount = uint64(len(t.table.keyframes))
		}
		out[i] = TrackInfo{
			TrackID:     t.id,
			Kind:        t.kind,
			Timescale:   t.timescale,
			Duration:    t.duration,
			SampleCount: t.table.sampleCount,
			KeyCount:    keyCount,
		}
	}
	return out
}

// MoovBeforeFirstMdat reports whether the moov box ends at or before the
// start of the first mdat box.
func (f *File) MoovBeforeFirstMdat() bool {
	if f.firstMdat < 0 {
		return true // no media data at all: index trivially "first"
	}
	return f.boxes[f.moovIdx].end() <= f.boxes[f.firstMdat].headerOff
}

// trackByIDLookup returns the track or ErrTrackNotFound.
func (f *File) trackByIDLookup(id uint32) (*track, error) {
	t, ok := f.trackByID[id]
	if !ok {
		return nil, ErrTrackNotFound
	}
	return t, nil
}
