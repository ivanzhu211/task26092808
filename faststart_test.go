package mp4post

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// compareFiles opens both files and checks that every track and every
// sample matches: bytes, DTS, keyframe flag.
func compareFiles(t *testing.T, srcPath, dstPath string) {
	t.Helper()
	src, err := Open(srcPath)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	defer src.Close()
	dst, err := Open(dstPath)
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}
	defer dst.Close()
	st, dt := src.Tracks(), dst.Tracks()
	if len(st) != len(dt) {
		t.Fatalf("track count %d vs %d", len(st), len(dt))
	}
	for i := range st {
		if st[i] != dt[i] {
			t.Errorf("track %d info differs: %+v vs %+v", i, st[i], dt[i])
		}
		for n := uint64(1); n <= st[i].SampleCount; n++ {
			ss, err := src.ReadSample(st[i].TrackID, n)
			if err != nil {
				t.Fatalf("src ReadSample(%d,%d): %v", st[i].TrackID, n, err)
			}
			ds, err := dst.ReadSample(dt[i].TrackID, n)
			if err != nil {
				t.Fatalf("dst ReadSample(%d,%d): %v", dt[i].TrackID, n, err)
			}
			if !bytes.Equal(ss.Data, ds.Data) || ss.DTS != ds.DTS || ss.Keyframe != ds.Keyframe {
				t.Errorf("track %d sample %d differs", st[i].TrackID, n)
			}
		}
	}
	if !dst.MoovBeforeFirstMdat() {
		t.Error("dst: moov is not before first mdat after FastStart")
	}
}

// checkBoxOrder verifies dst top-level boxes are src's boxes with moov
// moved to just before the first mdat; non-moov boxes byte-identical.
func checkBoxOrder(t *testing.T, srcPath, dstPath string) {
	t.Helper()
	srcData, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	dstData, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	srcBoxes, err := scanTopLevel(bytes.NewReader(srcData), int64(len(srcData)))
	if err != nil {
		t.Fatal(err)
	}
	dstBoxes, err := scanTopLevel(bytes.NewReader(dstData), int64(len(dstData)))
	if err != nil {
		t.Fatal(err)
	}
	if len(srcBoxes) != len(dstBoxes) {
		t.Fatalf("top-level box count %d vs %d", len(srcBoxes), len(dstBoxes))
	}
	// expected order: src order with moov removed, inserted before first mdat
	var srcMoov, srcFirstMdat int = -1, -1
	for i, b := range srcBoxes {
		switch string(b.typ[:]) {
		case "moov":
			srcMoov = i
		case "mdat":
			if srcFirstMdat < 0 {
				srcFirstMdat = i
			}
		}
	}
	var want []int // indices into srcBoxes
	for i := range srcBoxes {
		if i == srcFirstMdat {
			want = append(want, srcMoov)
		}
		if i == srcMoov {
			continue
		}
		want = append(want, i)
	}
	for i, sb := range dstBoxes {
		srcBox := srcBoxes[want[i]]
		if sb.typ != srcBox.typ {
			t.Errorf("dst box %d type %.4s, want %.4s", i, sb.typ, srcBox.typ)
		}
		if sb.typ == [4]byte{'m', 'o', 'o', 'v'} {
			continue // content changed (offsets), checked via samples
		}
		got := dstData[sb.headerOff:sb.end()]
		exp := srcData[srcBox.headerOff:srcBox.end()]
		if !bytes.Equal(got, exp) {
			t.Errorf("dst box %d (%.4s) content differs from src", i, sb.typ)
		}
	}
	// moov must be immediately before the first mdat in dst
	for i, b := range dstBoxes {
		if string(b.typ[:]) == "moov" {
			if i+1 >= len(dstBoxes) || string(dstBoxes[i+1].typ[:]) != "mdat" {
				t.Errorf("moov not immediately followed by mdat")
			}
			if b.end() != dstBoxes[i+1].headerOff {
				t.Errorf("gap between moov and first mdat")
			}
		}
	}
}

func runFastStart(t *testing.T, spec *fileSpec) {
	t.Helper()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.mp4")
	dstPath := filepath.Join(dir, "dst.mp4")
	buildFile(t, srcPath, spec)
	srcBefore, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := FastStart(srcPath, dstPath); err != nil {
		t.Fatalf("FastStart: %v", err)
	}
	compareFiles(t, srcPath, dstPath)
	checkBoxOrder(t, srcPath, dstPath)
	// source file must be untouched
	srcAfter, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(srcBefore, srcAfter) {
		t.Error("source file modified by FastStart")
	}
}

func TestFastStartMoovLast(t *testing.T) {
	runFastStart(t, &fileSpec{
		tracks: []trackSpec{videoTrack(), audioTrack()},
		layout: layoutMoovLast,
	})
}

func TestFastStartMoovMiddle(t *testing.T) {
	runFastStart(t, &fileSpec{
		tracks: []trackSpec{videoTrack(), audioTrack()},
		layout: layoutMoovMiddle,
	})
}

func TestFastStartAlreadyFastStart(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.mp4")
	dstPath := filepath.Join(dir, "dst.mp4")
	buildFile(t, srcPath, &fileSpec{tracks: []trackSpec{videoTrack()}, layout: layoutMoovFirst})
	err := FastStart(srcPath, dstPath)
	if !errors.Is(err, ErrAlreadyFastStart) {
		t.Fatalf("got %v, want ErrAlreadyFastStart", err)
	}
	if _, err := os.Stat(dstPath); !os.IsNotExist(err) {
		t.Error("dst file should not have been created")
	}
}

func TestFastStartBox64AndSize0(t *testing.T) {
	// 64-bit largesize headers everywhere; moov is the last box with size 0.
	runFastStart(t, &fileSpec{
		tracks:    []trackSpec{videoTrack(), audioTrack()},
		layout:    layoutMoovLast,
		box64:     true,
		size0Last: true,
	})
}

func TestFastStartSize0Mdat(t *testing.T) {
	// moov in the middle; the trailing mdat uses size 0 (extends to EOF).
	runFastStart(t, &fileSpec{
		tracks:    []trackSpec{videoTrack(), audioTrack()},
		layout:    layoutMoovMiddle,
		size0Last: true,
	})
}

func TestFastStartFreeBoxAndCo64(t *testing.T) {
	// free box before mdat; co64 chunk offsets; mdhd/tkhd version 1.
	v := videoTrack()
	v.co64 = true
	v.mdhdV1 = true
	v.tkhdV1 = true
	a := audioTrack()
	a.co64 = true
	runFastStart(t, &fileSpec{
		tracks:    []trackSpec{v, a},
		layout:    layoutMoovLast,
		freeBytes: 512,
	})
}

func TestFastStartSizeModes(t *testing.T) {
	for _, mode := range []sizeMode{szUniform, szList, sz4, sz8, sz16} {
		v := videoTrack()
		v.mode = mode
		if mode == szUniform {
			for i := range v.sizes {
				v.sizes[i] = 100
			}
		}
		if mode == sz4 || mode == sz8 || mode == sz16 {
			max := uint32(15)
			if mode == sz8 {
				max = 255
			} else if mode == sz16 {
				max = 65535
			}
			for i := range v.sizes {
				v.sizes[i] = v.sizes[i]%max + 1
			}
		}
		t.Run("", func(t *testing.T) {
			runFastStart(t, &fileSpec{
				tracks: []trackSpec{v, audioTrack()},
				layout: layoutMoovLast,
			})
		})
	}
}

func TestFastStartOtherTrackKind(t *testing.T) {
	meta := trackSpec{
		id:        3,
		handler:   "meta",
		timescale: 1000,
		stts:      [][2]uint32{{2, 500}},
		sizes:     []uint32{8, 9},
		mode:      szList,
		keyframes: nil,
		spc:       []uint32{2},
	}
	runFastStart(t, &fileSpec{
		tracks: []trackSpec{videoTrack(), meta},
		layout: layoutMoovLast,
	})
	f := openSpec(t, &fileSpec{
		tracks: []trackSpec{meta},
		layout: layoutMoovLast,
	})
	if got := f.Tracks()[0].Kind; got != TrackOther {
		t.Errorf("kind = %v, want other", got)
	}
}
