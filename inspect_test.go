package mp4post

import (
	"errors"
	"path/filepath"
	"testing"
)

func videoTrack() trackSpec {
	return trackSpec{
		id:        1,
		handler:   "vide",
		timescale: 90000,
		stts:      [][2]uint32{{4, 3000}, {2, 1500}},
		sizes:     []uint32{100, 20, 30, 40, 50, 60},
		mode:      szList,
		keyframes: []uint32{1, 4},
		spc:       []uint32{2, 3},
	}
}

func audioTrack() trackSpec {
	sizes := make([]uint32, 5)
	for i := range sizes {
		sizes[i] = 10
	}
	return trackSpec{
		id:        2,
		handler:   "soun",
		timescale: 48000,
		stts:      [][2]uint32{{5, 1024}},
		sizes:     sizes,
		mode:      szUniform,
		keyframes: nil, // no stss: every sample is a keyframe
		spc:       []uint32{5},
	}
}

func openSpec(t *testing.T, spec *fileSpec) *File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.mp4")
	buildFile(t, path, spec)
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestInspectMultiTrack(t *testing.T) {
	f := openSpec(t, &fileSpec{
		tracks: []trackSpec{videoTrack(), audioTrack()},
		layout: layoutMoovLast,
	})
	tracks := f.Tracks()
	if len(tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(tracks))
	}
	v, a := tracks[0], tracks[1]
	if v.TrackID != 1 || v.Kind != TrackVideo || v.Timescale != 90000 {
		t.Errorf("video track info wrong: %+v", v)
	}
	if v.SampleCount != 6 || v.KeyCount != 2 {
		t.Errorf("video counts wrong: %+v", v)
	}
	if v.Duration != 4*3000+2*1500 {
		t.Errorf("video duration wrong: %d", v.Duration)
	}
	if a.TrackID != 2 || a.Kind != TrackAudio || a.Timescale != 48000 {
		t.Errorf("audio track info wrong: %+v", a)
	}
	if a.SampleCount != 5 || a.KeyCount != 5 {
		t.Errorf("audio counts wrong (no stss => all key): %+v", a)
	}
	if f.MoovBeforeFirstMdat() {
		t.Error("moov-last file reported as faststart")
	}
}

func TestInspectMoovFirst(t *testing.T) {
	f := openSpec(t, &fileSpec{
		tracks: []trackSpec{videoTrack()},
		layout: layoutMoovFirst,
	})
	if !f.MoovBeforeFirstMdat() {
		t.Error("moov-first file not reported as faststart")
	}
}

func TestReadSampleBytesAndDTS(t *testing.T) {
	f := openSpec(t, &fileSpec{
		tracks: []trackSpec{videoTrack(), audioTrack()},
		layout: layoutMoovLast,
	})
	// video samples: sizes 100,20,30,40,50,60; stts (4,3000),(2,1500)
	wantDTS := []uint64{0, 3000, 6000, 9000, 12000, 13500}
	for n := uint64(1); n <= 6; n++ {
		s, err := f.ReadSample(1, n)
		if err != nil {
			t.Fatalf("ReadSample(1,%d): %v", n, err)
		}
		if s.DTS != wantDTS[n-1] {
			t.Errorf("sample %d DTS=%d want %d", n, s.DTS, wantDTS[n-1])
		}
		want := fillSample(1, int(n), len(s.Data))
		if len(s.Data) == 0 {
			t.Fatalf("sample %d empty", n)
		}
		for i := range want {
			if s.Data[i] != want[i] {
				t.Fatalf("sample %d byte %d = %d want %d", n, i, s.Data[i], want[i])
			}
		}
	}
	// keyframes: 1 and 4
	for n, want := range map[uint64]bool{1: true, 2: false, 3: false, 4: true, 5: false, 6: false} {
		s, _ := f.ReadSample(1, n)
		if s.Keyframe != want {
			t.Errorf("sample %d keyframe=%v want %v", n, s.Keyframe, want)
		}
	}
	// audio: uniform size, all keyframes
	s, err := f.ReadSample(2, 3)
	if err != nil {
		t.Fatalf("ReadSample(2,3): %v", err)
	}
	if !s.Keyframe || s.DTS != 2*1024 || len(s.Data) != 10 {
		t.Errorf("audio sample 3 wrong: %+v", s)
	}
}

func TestReadSampleErrors(t *testing.T) {
	f := openSpec(t, &fileSpec{tracks: []trackSpec{videoTrack()}, layout: layoutMoovLast})
	if _, err := f.ReadSample(99, 1); !errors.Is(err, ErrTrackNotFound) {
		t.Errorf("bad track: %v", err)
	}
	if _, err := f.ReadSample(1, 0); !errors.Is(err, ErrSampleNotFound) {
		t.Errorf("sample 0: %v", err)
	}
	if _, err := f.ReadSample(1, 7); !errors.Is(err, ErrSampleNotFound) {
		t.Errorf("sample 7: %v", err)
	}
}

func TestFindKeyframe(t *testing.T) {
	f := openSpec(t, &fileSpec{tracks: []trackSpec{videoTrack(), audioTrack()}, layout: layoutMoovLast})
	// video keyframes: sample 1 (DTS 0), sample 4 (DTS 9000)
	cases := []struct {
		tid  uint32
		ts   uint64
		want uint64
		err  error
	}{
		{1, 0, 1, nil},
		{1, 8999, 1, nil},
		{1, 9000, 4, nil},
		{1, 1 << 40, 4, nil},
		{2, 0, 1, nil},    // audio: all keyframes
		{2, 1024, 2, nil}, // last sample with DTS <= 1024
		{2, 4096, 5, nil},
		{2, 1 << 40, 5, nil},
	}
	for _, c := range cases {
		got, err := f.FindKeyframe(c.tid, c.ts)
		if !errors.Is(err, c.err) || got != c.want {
			t.Errorf("FindKeyframe(%d,%d) = %d,%v want %d,%v", c.tid, c.ts, got, err, c.want, c.err)
		}
	}
	if _, err := f.FindKeyframe(99, 0); !errors.Is(err, ErrTrackNotFound) {
		t.Errorf("bad track: %v", err)
	}
}

func TestInspectConvenience(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.mp4")
	buildFile(t, path, &fileSpec{tracks: []trackSpec{videoTrack()}, layout: layoutMoovFirst})
	r, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if !r.MoovBeforeFirstMdat || len(r.Tracks) != 1 {
		t.Errorf("unexpected report: %+v", r)
	}
}
