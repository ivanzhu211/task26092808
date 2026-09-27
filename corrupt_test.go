package mp4post

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func buildBytes(t *testing.T, spec *fileSpec) []byte {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ok.mp4")
	buildFile(t, p, spec)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func expectOpenErr(t *testing.T, data []byte, want error) {
	t.Helper()
	p := writeFile(t, "bad.mp4", data)
	_, err := Open(p)
	if !errors.Is(err, want) {
		t.Fatalf("Open error = %v, want %v", err, want)
	}
}

func TestCorruptGarbage(t *testing.T) {
	expectOpenErr(t, []byte("this is not an mp4 file at all, just text"), ErrInvalidStructure)
}

func TestCorruptEmpty(t *testing.T) {
	_, err := Open(writeFile(t, "empty.mp4", nil))
	if !errors.Is(err, ErrNoMoov) {
		t.Fatalf("got %v, want ErrNoMoov", err)
	}
}

func TestCorruptTruncated(t *testing.T) {
	good := buildBytes(t, &fileSpec{tracks: []trackSpec{videoTrack()}, layout: layoutMoovLast})
	for _, n := range []int{4, 7, 18, len(good) / 2, len(good) - 3} {
		expectOpenErr(t, good[:n], ErrInvalidStructure)
	}
}

func TestCorruptBoxSizeTooBig(t *testing.T) {
	good := buildBytes(t, &fileSpec{tracks: []trackSpec{videoTrack()}, layout: layoutMoovLast})
	// inflate the ftyp size field beyond EOF
	good[0] = 0x7f
	good[1] = 0xff
	good[2] = 0xff
	good[3] = 0xff
	expectOpenErr(t, good, ErrInvalidStructure)
}

func TestFragmentedMoof(t *testing.T) {
	good := buildBytes(t, &fileSpec{tracks: []trackSpec{videoTrack()}, layout: layoutMoovLast})
	moof := box("moof", box("mfhd", []byte{0, 0, 0, 0, 0, 0, 0, 1}))
	expectOpenErr(t, append(good, moof...), ErrFragmented)
}

func TestFragmentedMvex(t *testing.T) {
	good := buildBytes(t, &fileSpec{tracks: []trackSpec{videoTrack()}, layout: layoutMoovLast})
	// find moov header (last box) and inject an mvex child: rebuild moov
	// by appending mvex payload and fixing the moov size.
	moovStart := len(good) - 8
	for moovStart > 0 && string(good[moovStart+4:moovStart+8]) != "moov" {
		moovStart--
	}
	if moovStart <= 0 {
		t.Fatal("moov not found")
	}
	moovPayload := good[moovStart+8:]
	mvex := box("mvex", nil)
	newMoov := box("moov", append(moovPayload, mvex...))
	bad := append(good[:moovStart], newMoov...)
	expectOpenErr(t, bad, ErrFragmented)
}

func TestCompressedMoov(t *testing.T) {
	good := buildBytes(t, &fileSpec{tracks: []trackSpec{videoTrack()}, layout: layoutMoovLast})
	cmov := box("cmov", []byte{0, 0, 0, 0})
	expectOpenErr(t, append(cmov, good...), ErrCompressedMoov)
}

func TestNoMoov(t *testing.T) {
	data := append(box("ftyp", []byte("isom")), box("mdat", []byte{1, 2, 3})...)
	expectOpenErr(t, data, ErrNoMoov)
}

func TestMissingStsz(t *testing.T) {
	v := videoTrack()
	spec := &fileSpec{tracks: []trackSpec{v}, layout: layoutMoovLast}
	good := buildBytes(t, spec)
	// corrupt the stsz box type to something unknown so stbl has no sizes
	idx := indexOf(good, []byte("stsz"))
	if idx < 0 {
		t.Fatal("stsz not found")
	}
	copy(good[idx:], "stsz") // sanity
	copy(good[idx:], "xxxx")
	expectOpenErr(t, good, ErrInvalidStructure)
}

func TestBadStsc(t *testing.T) {
	good := buildBytes(t, &fileSpec{tracks: []trackSpec{videoTrack()}, layout: layoutMoovLast})
	idx := indexOf(good, []byte("stsc"))
	if idx < 0 {
		t.Fatal("stsc not found")
	}
	// first entry's first_chunk field: stsc payload = v/f(4) count(4) then first_chunk
	firstChunk := idx + 4 + 8
	good[firstChunk+3] = 0 // set first_chunk to 0
	expectOpenErr(t, good, ErrInvalidStructure)
}

func TestReadSampleBeyondEOF(t *testing.T) {
	good := buildBytes(t, &fileSpec{tracks: []trackSpec{videoTrack()}, layout: layoutMoovFirst})
	// point the first chunk offset far beyond EOF
	idx := indexOf(good, []byte("stco"))
	if idx < 0 {
		t.Fatal("stco not found")
	}
	off := idx + 4 + 8 // skip type, version/flags, count
	good[off+2] = 0xff
	good[off+3] = 0xff
	p := writeFile(t, "badoff.mp4", good)
	f, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	if _, err := f.ReadSample(1, 1); !errors.Is(err, ErrInvalidStructure) {
		t.Fatalf("got %v, want ErrInvalidStructure", err)
	}
}

func TestFastStartCorruptLeavesNoOutput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	dst := filepath.Join(dir, "dst.mp4")
	if err := os.WriteFile(src, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := FastStart(src, dst); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("dst should not exist after failed FastStart")
	}
	// temp files must be cleaned up
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "src.mp4" {
			t.Errorf("leftover file %q", e.Name())
		}
	}
}

func TestFastStartSamePath(t *testing.T) {
	p := writeFile(t, "x.mp4", buildBytes(t, &fileSpec{tracks: []trackSpec{videoTrack()}, layout: layoutMoovLast}))
	if err := FastStart(p, p); err == nil {
		t.Fatal("expected error for src == dst")
	}
}

func indexOf(b, sub []byte) int {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == string(sub) {
			return i
		}
	}
	return -1
}
