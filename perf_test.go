package mp4post

import (
	"bufio"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildPerfFile writes a single-track MP4 with n samples of 4 bytes each,
// 1000 samples per chunk, keyframes every 250 samples.
func buildPerfFile(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "perf.mp4")

	spc := 1000
	numChunks := n / spc
	sizes := make([]uint32, n)
	for i := range sizes {
		sizes[i] = 4
	}
	var keyframes []uint32
	for k := 1; k <= n; k += 250 {
		keyframes = append(keyframes, uint32(k))
	}
	ts := trackSpec{
		id:        1,
		handler:   "vide",
		timescale: 90000,
		stts:      [][2]uint32{{uint32(n), 100}},
		sizes:     sizes,
		mode:      szUniform,
		keyframes: keyframes,
		spc:       []uint32{uint32(spc)},
	}
	spec := &fileSpec{tracks: []trackSpec{ts}}

	ftyp := box("ftyp", append([]byte("isom"), append(u32(0), []byte("isom")...)...))
	mdatHdrLen := int64(8)
	offsets := make([]uint64, numChunks)
	for c := 0; c < numChunks; c++ {
		offsets[c] = uint64(int64(len(ftyp)) + mdatHdrLen + int64(c*spc*4))
	}
	moov := buildMoov(spec, [][]uint64{offsets})

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	if _, err := w.Write(ftyp); err != nil {
		t.Fatal(err)
	}
	mdatPayload := n * 4
	hdr := boxHeaderBytes("mdat", mdatPayload, false, false)
	if _, err := w.Write(hdr); err != nil {
		t.Fatal(err)
	}
	sample := []byte{1, 2, 3, 4}
	for i := 0; i < n; i++ {
		if _, err := w.Write(sample); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Write(moov); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRandomReadPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping performance test in -short mode")
	}
	const n = 2_000_000
	const reads = 10_000
	path := buildPerfFile(t, n)

	start := time.Now()
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	openDur := time.Since(start)
	t.Logf("Open (2M samples): %v", openDur)

	rng := rand.New(rand.NewSource(42))
	start = time.Now()
	var dtsSum uint64
	for i := 0; i < reads; i++ {
		sn := uint64(rng.Int63n(n) + 1)
		s, err := f.ReadSample(1, sn)
		if err != nil {
			t.Fatalf("ReadSample(%d): %v", sn, err)
		}
		if len(s.Data) != 4 {
			t.Fatalf("sample %d size %d", sn, len(s.Data))
		}
		dtsSum += s.DTS
	}
	readDur := time.Since(start)
	f.Close()
	t.Logf("%d random reads: %v", reads, readDur)
	if readDur > time.Second {
		t.Fatalf("%d random reads took %v, want < 1s", reads, readDur)
	}
	_ = dtsSum
}

func TestFastStartLargeIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-index test in -short mode")
	}
	const n = 2_000_000
	src := buildPerfFile(t, n)
	dst := filepath.Join(t.TempDir(), "dst.mp4")
	if err := FastStart(src, dst); err != nil {
		t.Fatalf("FastStart: %v", err)
	}
	sf, err := Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Close()
	df, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer df.Close()
	if !df.MoovBeforeFirstMdat() {
		t.Fatal("dst moov not before first mdat")
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 1000; i++ {
		sn := uint64(rng.Int63n(n) + 1)
		ss, err := sf.ReadSample(1, sn)
		if err != nil {
			t.Fatal(err)
		}
		ds, err := df.ReadSample(1, sn)
		if err != nil {
			t.Fatal(err)
		}
		if ss.DTS != ds.DTS || ss.Keyframe != ds.Keyframe || len(ss.Data) != len(ds.Data) {
			t.Fatalf("sample %d differs", sn)
		}
	}
}
