package mp4post

import (
	"encoding/binary"
	"os"
	"testing"
)

// sizeMode selects how sample sizes are stored.
type sizeMode int

const (
	szUniform sizeMode = iota // stsz with a single uniform size
	szList                    // stsz with per-sample entries
	sz4                       // stz2, 4-bit fields
	sz8                       // stz2, 8-bit fields
	sz16                      // stz2, 16-bit fields
)

type layoutMode int

const (
	layoutMoovLast   layoutMode = iota // ftyp [free] mdat... moov
	layoutMoovMiddle                   // ftyp mdat moov mdat
	layoutMoovFirst                    // ftyp moov mdat
)

type trackSpec struct {
	id        uint32
	handler   string // "vide", "soun", anything else -> other
	timescale uint32
	mdhdV1    bool
	tkhdV1    bool
	duration  uint64      // mdhd duration; 0 -> derived from stts
	stts      [][2]uint32 // (count, delta) pairs
	sizes     []uint32    // per-sample sizes
	mode      sizeMode
	keyframes []uint32 // 1-based; nil -> no stss box
	spc       []uint32 // samples-per-chunk pattern for stsc
	co64      bool
}

type fileSpec struct {
	tracks    []trackSpec
	layout    layoutMode
	box64     bool  // top-level boxes use 64-bit largesize headers
	size0Last bool  // last top-level box uses size==0 (extends to EOF)
	freeBytes int64 // if > 0, insert a free box of this size after ftyp
}

// sampleByte generates deterministic content for track t, sample n (1-based).
func sampleByte(t uint32, n int, i int) byte {
	return byte((uint32(i)*7 + t*131 + uint32(n)*29) & 0xff)
}

func fillSample(t uint32, n int, size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = sampleByte(t, n, i)
	}
	return b
}

// boxHeaderBytes encodes a box header; if force64 the largesize form is
// used, if size0 the size field is 0 (box extends to EOF).
func boxHeaderBytes(typ string, payloadLen int, force64, size0 bool) []byte {
	if size0 {
		out := make([]byte, 8)
		copy(out[4:8], typ)
		return out
	}
	if force64 {
		out := make([]byte, 16)
		binary.BigEndian.PutUint32(out[0:4], 1)
		copy(out[4:8], typ)
		binary.BigEndian.PutUint64(out[8:16], uint64(payloadLen)+16)
		return out
	}
	out := make([]byte, 8)
	binary.BigEndian.PutUint32(out[0:4], uint32(payloadLen)+8)
	copy(out[4:8], typ)
	return out
}

func box(typ string, payload []byte) []byte {
	out := boxHeaderBytes(typ, len(payload), false, false)
	return append(out, payload...)
}

func fullBox(typ string, version byte, payload []byte) []byte {
	p := append([]byte{version, 0, 0, 0}, payload...)
	return box(typ, p)
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// chunkCounts splits a track's samples into chunks per the spc pattern.
func (ts *trackSpec) chunkCounts() []int {
	remaining := len(ts.sizes)
	var chunks []int
	i := 0
	for remaining > 0 {
		spc := int(ts.spc[len(ts.spc)-1])
		if i < len(ts.spc) {
			spc = int(ts.spc[i])
		}
		if spc > remaining {
			spc = remaining
		}
		chunks = append(chunks, spc)
		remaining -= spc
		i++
	}
	return chunks
}

func (ts *trackSpec) sttsDuration() uint64 {
	var d uint64
	for _, e := range ts.stts {
		d += uint64(e[0]) * uint64(e[1])
	}
	return d
}

func (ts *trackSpec) mdhdDuration() uint64 {
	if ts.duration != 0 {
		return ts.duration
	}
	return ts.sttsDuration()
}

func buildStts(ts *trackSpec) []byte {
	p := u32(uint32(len(ts.stts)))
	for _, e := range ts.stts {
		p = append(p, u32(e[0])...)
		p = append(p, u32(e[1])...)
	}
	return fullBox("stts", 0, p)
}

func buildStss(ts *trackSpec) []byte {
	if ts.keyframes == nil {
		return nil
	}
	p := u32(uint32(len(ts.keyframes)))
	for _, k := range ts.keyframes {
		p = append(p, u32(k)...)
	}
	return fullBox("stss", 0, p)
}

func buildStsc(ts *trackSpec) []byte {
	p := u32(uint32(len(ts.spc)))
	for i, spc := range ts.spc {
		p = append(p, u32(uint32(i+1))...)
		p = append(p, u32(spc)...)
		p = append(p, u32(1)...)
	}
	return fullBox("stsc", 0, p)
}

func buildStsz(ts *trackSpec) []byte {
	switch ts.mode {
	case szUniform:
		sz := uint32(0)
		if len(ts.sizes) > 0 {
			sz = ts.sizes[0]
		}
		return fullBox("stsz", 0, append(u32(sz), u32(uint32(len(ts.sizes)))...))
	case szList:
		p := append(u32(0), u32(uint32(len(ts.sizes)))...)
		for _, s := range ts.sizes {
			p = append(p, u32(s)...)
		}
		return fullBox("stsz", 0, p)
	case sz4:
		p := append([]byte{0, 0, 0, 4}, u32(uint32(len(ts.sizes)))...)
		for i := 0; i < len(ts.sizes); i += 2 {
			hi := byte(ts.sizes[i])
			lo := byte(0)
			if i+1 < len(ts.sizes) {
				lo = byte(ts.sizes[i+1])
			}
			p = append(p, hi<<4|lo)
		}
		return fullBox("stz2", 0, p)
	case sz8:
		p := append([]byte{0, 0, 0, 8}, u32(uint32(len(ts.sizes)))...)
		for _, s := range ts.sizes {
			p = append(p, byte(s))
		}
		return fullBox("stz2", 0, p)
	case sz16:
		p := append([]byte{0, 0, 0, 16}, u32(uint32(len(ts.sizes)))...)
		for _, s := range ts.sizes {
			b := make([]byte, 2)
			binary.BigEndian.PutUint16(b, uint16(s))
			p = append(p, b...)
		}
		return fullBox("stz2", 0, p)
	}
	panic("bad mode")
}

func buildStco(ts *trackSpec, offsets []uint64) []byte {
	p := u32(uint32(len(offsets)))
	if ts.co64 {
		for _, o := range offsets {
			p = append(p, u64(o)...)
		}
		return fullBox("co64", 0, p)
	}
	for _, o := range offsets {
		p = append(p, u32(uint32(o))...)
	}
	return fullBox("stco", 0, p)
}

func buildTkhd(ts *trackSpec) []byte {
	if ts.tkhdV1 {
		p := append(u64(0), u64(0)...) // creation, modification
		p = append(p, u32(ts.id)...)
		p = append(p, u32(0)...) // reserved
		p = append(p, u64(ts.mdhdDuration())...)
		p = append(p, make([]byte, 8+36+8)...) // reserved, matrix, width/height
		return fullBox("tkhd", 1, p)
	}
	p := append(u32(0), u32(0)...) // creation, modification
	p = append(p, u32(ts.id)...)
	p = append(p, u32(0)...) // reserved
	p = append(p, u32(uint32(ts.mdhdDuration()))...)
	p = append(p, make([]byte, 8+36+8)...)
	return fullBox("tkhd", 0, p)
}

func buildMdhd(ts *trackSpec) []byte {
	if ts.mdhdV1 {
		p := append(u64(0), u64(0)...)
		p = append(p, u32(ts.timescale)...)
		p = append(p, u64(ts.mdhdDuration())...)
		p = append(p, make([]byte, 4)...) // language + pre_defined
		return fullBox("mdhd", 1, p)
	}
	p := append(u32(0), u32(0)...)
	p = append(p, u32(ts.timescale)...)
	p = append(p, u32(uint32(ts.mdhdDuration()))...)
	p = append(p, make([]byte, 4)...)
	return fullBox("mdhd", 0, p)
}

func buildHdlr(ts *trackSpec) []byte {
	p := u32(0) // pre_defined
	p = append(p, []byte(ts.handler)...)
	p = append(p, make([]byte, 12)...) // reserved
	p = append(p, 0)                   // name
	return fullBox("hdlr", 0, p)
}

// buildTrak builds the trak box; offsets are the track's chunk offsets.
func buildTrak(ts *trackSpec, offsets []uint64) []byte {
	stbl := buildStts(ts)
	if s := buildStss(ts); s != nil {
		stbl = append(stbl, s...)
	}
	stbl = append(stbl, buildStsc(ts)...)
	stbl = append(stbl, buildStsz(ts)...)
	stbl = append(stbl, buildStco(ts, offsets)...)
	minf := box("minf", box("stbl", stbl))
	mdia := box("mdia", append(append(buildMdhd(ts), buildHdlr(ts)...), minf...))
	return box("trak", append(buildTkhd(ts), mdia...))
}

func buildMoov(spec *fileSpec, offsets [][]uint64) []byte {
	mvhd := fullBox("mvhd", 0, append(append(u32(0), u32(0)...), append(u32(1000), u32(0)...)...))
	payload := mvhd
	for i := range spec.tracks {
		payload = append(payload, buildTrak(&spec.tracks[i], offsets[i])...)
	}
	return box("moov", payload)
}

// chunkLayout assigns every chunk of every track (round-robin) to mdat
// groups and returns, per track, the chunk offsets once positions are known.
type chunkRef struct {
	track    int
	chunkIdx int // index within the track
	count    int // samples in this chunk
}

func orderedChunks(spec *fileSpec) []chunkRef {
	counts := make([][]int, len(spec.tracks))
	maxChunks := 0
	for i := range spec.tracks {
		counts[i] = spec.tracks[i].chunkCounts()
		if len(counts[i]) > maxChunks {
			maxChunks = len(counts[i])
		}
	}
	var out []chunkRef
	for c := 0; c < maxChunks; c++ {
		for t := range spec.tracks {
			if c < len(counts[t]) {
				out = append(out, chunkRef{track: t, chunkIdx: c, count: counts[t][c]})
			}
		}
	}
	return out
}

// buildFile writes the MP4 described by spec to path and returns the
// expected per-track chunk offsets.
func buildFile(t *testing.T, path string, spec *fileSpec) {
	t.Helper()
	chunks := orderedChunks(spec)

	// Split chunks into mdat groups.
	var groups [][]chunkRef
	switch spec.layout {
	case layoutMoovMiddle:
		half := (len(chunks) + 1) / 2
		groups = [][]chunkRef{chunks[:half], chunks[half:]}
	default:
		groups = [][]chunkRef{chunks}
	}

	ftyp := box("ftyp", append([]byte("isom"), append(u32(0), []byte("isom")...)...))
	var freeBox []byte
	if spec.freeBytes > 0 {
		hdrLen := 8
		if spec.box64 {
			hdrLen = 16
		}
		freeBox = boxHeaderBytes("free", int(spec.freeBytes)-hdrLen, spec.box64, false)
		freeBox = append(freeBox, make([]byte, int(spec.freeBytes)-len(freeBox))...)
	}

	hdrLen := int64(8)
	if spec.box64 {
		hdrLen = 16
	}

	// moov size does not depend on offset values, so one pass suffices.
	offsets := make([][]uint64, len(spec.tracks))
	for i := range spec.tracks {
		offsets[i] = make([]uint64, len(spec.tracks[i].chunkCounts()))
	}
	moovIsSize0 := spec.size0Last && spec.layout == layoutMoovLast
	moovBytes := buildMoov(spec, offsets)
	moovTotal := int64(len(moovBytes))
	if spec.box64 {
		moovTotal += 8
	}
	// Assign chunk offsets by walking the top-level sequence.
	pos := int64(len(ftyp) + len(freeBox))
	if spec.layout == layoutMoovFirst {
		pos += moovTotal
	}
	for gi, g := range groups {
		cur := pos + hdrLen
		for _, c := range g {
			offsets[c.track][c.chunkIdx] = uint64(cur)
			cur += chunkBytes(spec, c)
		}
		pos = cur
		if spec.layout == layoutMoovMiddle && gi == 0 {
			pos += moovTotal
		}
	}

	// Real moov with final offsets.
	moovBytes = buildMoov(spec, offsets)
	moovHdr := boxHeaderBytes("moov", len(moovBytes)-8, spec.box64, moovIsSize0)

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	write := func(b []byte) {
		if _, err := f.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	write(ftyp)
	write(freeBox)
	if spec.layout == layoutMoovFirst {
		write(moovHdr)
		write(moovBytes[8:])
	}
	sampleCursor := make([]int, len(spec.tracks))
	for gi, g := range groups {
		if spec.layout == layoutMoovMiddle && gi == 1 {
			write(moovHdr)
			write(moovBytes[8:])
		}
		mdatIsSize0 := spec.size0Last && spec.layout == layoutMoovMiddle && gi == len(groups)-1
		hdrPos, _ := f.Seek(0, 1)
		write(boxHeaderBytes("mdat", 0, spec.box64, mdatIsSize0))
		for _, c := range g {
			ts := &spec.tracks[c.track]
			for i := 0; i < c.count; i++ {
				n := sampleCursor[c.track]
				sampleCursor[c.track]++
				write(fillSample(ts.id, n+1, int(ts.sizes[n])))
			}
		}
		if !mdatIsSize0 {
			cur, _ := f.Seek(0, 1)
			payloadLen := cur - hdrPos - hdrLen
			if _, err := f.Seek(hdrPos, 0); err != nil {
				t.Fatal(err)
			}
			write(boxHeaderBytes("mdat", int(payloadLen), spec.box64, false))
			if _, err := f.Seek(cur, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	if spec.layout == layoutMoovLast {
		write(moovHdr)
		write(moovBytes[8:])
	}
}

// chunkBytes returns the total payload bytes of one chunk.
func chunkBytes(spec *fileSpec, c chunkRef) int64 {
	ts := &spec.tracks[c.track]
	base := 0
	for i, cc := range ts.chunkCounts() {
		if i == c.chunkIdx {
			break
		}
		base += cc
	}
	var sz int64
	for _, s := range ts.sizes[base : base+c.count] {
		sz += int64(s)
	}
	return sz
}

// groupSize returns the total bytes (header + payload) of one mdat group.
func groupSize(spec *fileSpec, g []chunkRef, hdrLen int64) int64 {
	sz := hdrLen
	for _, c := range g {
		sz += chunkBytes(spec, c)
	}
	return sz
}
