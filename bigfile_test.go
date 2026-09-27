package mp4post

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

const fourGiB = int64(1) << 32

// buildSparseBigFile creates an MP4 whose mdat payload is mostly a sparse
// hole, with real sample bytes written at the given chunk offsets.
// Returns the source path and the moov bytes used.
func buildSparseBigFile(t *testing.T, spec *fileSpec, mdatPayload int64, chunkOffsets []uint64, moovExtra []byte) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "big.mp4")

	ftyp := box("ftyp", append([]byte("isom"), append(u32(0), []byte("isom")...)...))
	mdatHdr := boxHeaderBytes("mdat", int(mdatPayload), true, false)
	moov := buildMoov(spec, [][]uint64{chunkOffsets})
	if len(moovExtra) > 0 {
		// pad moov with a free child box to reach the desired size
		payload := moov[8:]
		payload = append(payload, box("free", make([]byte, len(moovExtra)))...)
		moov = box("moov", payload)
	}

	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(ftyp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(mdatHdr); err != nil {
		t.Fatal(err)
	}
	// real sample bytes at each chunk
	ts := &spec.tracks[0]
	n := 0
	for ci, cnt := range ts.chunkCounts() {
		var data []byte
		for i := 0; i < cnt; i++ {
			data = append(data, fillSample(ts.id, n+1, int(ts.sizes[n]))...)
			n++
		}
		if _, err := f.WriteAt(data, int64(chunkOffsets[ci])); err != nil {
			t.Fatal(err)
		}
	}
	moovPos := int64(len(ftyp)+len(mdatHdr)) + mdatPayload
	if _, err := f.WriteAt(moov, moovPos); err != nil {
		t.Fatal(err)
	}
	return src
}

func bigTrackSpec(co64 bool) *fileSpec {
	return &fileSpec{
		tracks: []trackSpec{{
			id:        1,
			handler:   "vide",
			timescale: 90000,
			stts:      [][2]uint32{{4, 3000}},
			sizes:     []uint32{100, 20, 30, 40},
			mode:      szList,
			keyframes: []uint32{1, 3},
			spc:       []uint32{2, 2},
			co64:      co64,
		}},
	}
}

// Samples already beyond 4 GiB in the source (co64).
func TestFastStartAlreadyCrossing4GiB(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 4 GiB test in -short mode")
	}
	spec := bigTrackSpec(true)
	mdatPayload := fourGiB + 1<<20
	chunk0 := uint64(20 + 16) // ftyp + 64-bit mdat header
	chunk1 := uint64(fourGiB + 65536)
	src := buildSparseBigFile(t, spec, mdatPayload, []uint64{chunk0, chunk1}, nil)
	dst := filepath.Join(t.TempDir(), "dst.mp4")
	if err := FastStart(src, dst); err != nil {
		t.Fatalf("FastStart: %v", err)
	}
	compareFiles(t, src, dst)
}

// Samples below 4 GiB in the source (stco), pushed past 4 GiB only by
// moving moov forward: stco must be upgraded to co64.
func TestFastStartCrossing4GiBAfterMove(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 4 GiB test in -short mode")
	}
	spec := bigTrackSpec(false)
	mdatPayload := fourGiB + 1<<20
	chunk0 := uint64(20 + 16)
	chunk1 := uint64(fourGiB - 300) // fits in stco; moov shift pushes it over
	src := buildSparseBigFile(t, spec, mdatPayload, []uint64{chunk0, chunk1}, make([]byte, 1024))
	dst := filepath.Join(t.TempDir(), "dst.mp4")
	if err := FastStart(src, dst); err != nil {
		t.Fatalf("FastStart: %v", err)
	}
	compareFiles(t, src, dst)

	// the destination index must use co64 now
	df, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer df.Close()
	moovPayload, err := readPayload(df.f, df.boxes[df.moovIdx])
	if err != nil {
		t.Fatal(err)
	}
	if indexOf(moovPayload, []byte("co64")) < 0 {
		t.Error("dst moov does not contain co64 after crossing 4 GiB")
	}
	if indexOf(moovPayload, []byte("stco")) >= 0 {
		t.Error("dst moov still contains stco after crossing 4 GiB")
	}
	// chunk offsets must point at the real sample bytes
	s, err := df.ReadSample(1, 3)
	if err != nil {
		t.Fatalf("ReadSample after crossing: %v", err)
	}
	want := fillSample(1, 3, 30)
	if !bytes.Equal(s.Data, want) {
		t.Error("sample 3 bytes wrong after 4 GiB crossing")
	}
}
