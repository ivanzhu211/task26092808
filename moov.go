package mp4post

import (
	"encoding/binary"
	"fmt"
)

// track is one parsed track: metadata plus its sample table.
type track struct {
	id        uint32
	kind      TrackKind
	timescale uint32
	duration  uint64
	table     *sampleTable
}

// boxView is a zero-copy view of a box inside a parsed byte slice.
type boxView struct {
	typ     [4]byte
	payload []byte
}

// iterBoxes walks child boxes inside a container payload.
func iterBoxes(payload []byte, fn func(b boxView) error) error {
	off := 0
	for off < len(payload) {
		if len(payload)-off < 8 {
			return fmt.Errorf("%w: truncated child box header", ErrInvalidStructure)
		}
		size32 := binary.BigEndian.Uint32(payload[off : off+4])
		var typ [4]byte
		copy(typ[:], payload[off+4:off+8])
		var size int
		hdr := 8
		switch size32 {
		case 0:
			size = len(payload) - off
		case 1:
			if len(payload)-off < 16 {
				return fmt.Errorf("%w: truncated largesize child box", ErrInvalidStructure)
			}
			size64 := binary.BigEndian.Uint64(payload[off+8 : off+16])
			if size64 > uint64(len(payload)-off) {
				return fmt.Errorf("%w: child box %.4s size exceeds parent", ErrInvalidStructure, typ)
			}
			size = int(size64)
			hdr = 16
		default:
			size = int(size32)
		}
		if size < hdr || off+size > len(payload) {
			return fmt.Errorf("%w: child box %.4s size %d out of bounds", ErrInvalidStructure, typ, size)
		}
		if err := fn(boxView{typ: typ, payload: payload[off+hdr : off+size]}); err != nil {
			return err
		}
		off += size
	}
	return nil
}

// findChild returns the payload of the first child box of the given type,
// or nil if absent.
func findChild(payload []byte, want string) ([]byte, error) {
	var out []byte
	err := iterBoxes(payload, func(b boxView) error {
		if string(b.typ[:]) == want && out == nil {
			out = b.payload
		}
		return nil
	})
	return out, err
}

// fullboxVersion reads the version byte of a fullbox payload.
func fullboxVersion(payload []byte) (byte, error) {
	if len(payload) < 4 {
		return 0, fmt.Errorf("%w: truncated fullbox header", ErrInvalidStructure)
	}
	return payload[0], nil
}

// parseMoov parses the moov payload into tracks.
func parseMoov(payload []byte) ([]*track, error) {
	var tracks []*track
	err := iterBoxes(payload, func(b boxView) error {
		switch string(b.typ[:]) {
		case "trak":
			t, err := parseTrak(b.payload)
			if err != nil {
				return err
			}
			tracks = append(tracks, t)
		case "mvex":
			return ErrFragmented
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(tracks) == 0 {
		return nil, fmt.Errorf("%w: moov contains no tracks", ErrInvalidStructure)
	}
	return tracks, nil
}

func parseTrak(payload []byte) (*track, error) {
	t := &track{table: &sampleTable{}}
	tkhd, err := findChild(payload, "tkhd")
	if err != nil {
		return nil, err
	}
	if tkhd == nil {
		return nil, fmt.Errorf("%w: trak missing tkhd", ErrInvalidStructure)
	}
	if err := parseTkhd(tkhd, t); err != nil {
		return nil, err
	}
	mdia, err := findChild(payload, "mdia")
	if err != nil {
		return nil, err
	}
	if mdia == nil {
		return nil, fmt.Errorf("%w: trak missing mdia", ErrInvalidStructure)
	}
	if err := parseMdia(mdia, t); err != nil {
		return nil, err
	}
	t.table.timescale = t.timescale
	t.table.duration = t.duration
	t.table.finalize()
	return t, nil
}

func parseTkhd(p []byte, t *track) error {
	v, err := fullboxVersion(p)
	if err != nil {
		return err
	}
	switch v {
	case 0:
		if len(p) < 16 {
			return fmt.Errorf("%w: truncated tkhd", ErrInvalidStructure)
		}
		t.id = binary.BigEndian.Uint32(p[12:16])
	case 1:
		if len(p) < 24 {
			return fmt.Errorf("%w: truncated tkhd v1", ErrInvalidStructure)
		}
		t.id = binary.BigEndian.Uint32(p[20:24])
	default:
		return fmt.Errorf("%w: unsupported tkhd version %d", ErrInvalidStructure, v)
	}
	return nil
}

func parseMdia(p []byte, t *track) error {
	mdhd, err := findChild(p, "mdhd")
	if err != nil {
		return err
	}
	if mdhd == nil {
		return fmt.Errorf("%w: mdia missing mdhd", ErrInvalidStructure)
	}
	if err := parseMdhd(mdhd, t); err != nil {
		return err
	}
	hdlr, err := findChild(p, "hdlr")
	if err != nil {
		return err
	}
	if hdlr != nil && len(hdlr) >= 12 {
		switch string(hdlr[8:12]) {
		case "vide":
			t.kind = TrackVideo
		case "soun":
			t.kind = TrackAudio
		}
	}
	minf, err := findChild(p, "minf")
	if err != nil {
		return err
	}
	if minf == nil {
		return fmt.Errorf("%w: mdia missing minf", ErrInvalidStructure)
	}
	stbl, err := findChild(minf, "stbl")
	if err != nil {
		return err
	}
	if stbl == nil {
		return fmt.Errorf("%w: minf missing stbl", ErrInvalidStructure)
	}
	return parseStbl(stbl, t.table)
}

func parseMdhd(p []byte, t *track) error {
	v, err := fullboxVersion(p)
	if err != nil {
		return err
	}
	switch v {
	case 0:
		if len(p) < 20 {
			return fmt.Errorf("%w: truncated mdhd", ErrInvalidStructure)
		}
		t.timescale = binary.BigEndian.Uint32(p[12:16])
		t.duration = uint64(binary.BigEndian.Uint32(p[16:20]))
	case 1:
		if len(p) < 32 {
			return fmt.Errorf("%w: truncated mdhd v1", ErrInvalidStructure)
		}
		t.timescale = binary.BigEndian.Uint32(p[20:24])
		t.duration = binary.BigEndian.Uint64(p[24:32])
	default:
		return fmt.Errorf("%w: unsupported mdhd version %d", ErrInvalidStructure, v)
	}
	if t.timescale == 0 {
		return fmt.Errorf("%w: mdhd timescale is zero", ErrInvalidStructure)
	}
	return nil
}

func parseStbl(p []byte, st *sampleTable) error {
	var haveSizes, haveChunks, haveSTSC bool
	err := iterBoxes(p, func(b boxView) error {
		var err error
		switch string(b.typ[:]) {
		case "stts":
			err = parseStts(b.payload, st)
		case "stss":
			err = parseStss(b.payload, st)
		case "stsc":
			err = parseStsc(b.payload, st)
			haveSTSC = err == nil
		case "stsz":
			err = parseStsz(b.payload, st)
			haveSizes = err == nil
		case "stz2":
			err = parseStz2(b.payload, st)
			haveSizes = err == nil
		case "stco":
			err = parseStco(b.payload, st)
			haveChunks = err == nil
		case "co64":
			err = parseCo64(b.payload, st)
			haveChunks = err == nil
		}
		return err
	})
	if err != nil {
		return err
	}
	if !haveSizes {
		return fmt.Errorf("%w: stbl missing stsz/stz2", ErrInvalidStructure)
	}
	if !haveChunks {
		return fmt.Errorf("%w: stbl missing stco/co64", ErrInvalidStructure)
	}
	if !haveSTSC && st.sampleCount > 0 {
		return fmt.Errorf("%w: stbl missing stsc", ErrInvalidStructure)
	}
	return nil
}

func parseStts(p []byte, st *sampleTable) error {
	if len(p) < 8 {
		return fmt.Errorf("%w: truncated stts", ErrInvalidStructure)
	}
	n := binary.BigEndian.Uint32(p[4:8])
	if len(p) < 8+int(n)*8 {
		return fmt.Errorf("%w: truncated stts entries", ErrInvalidStructure)
	}
	st.stts = make([]sttsEntry, n)
	for i := range st.stts {
		st.stts[i] = sttsEntry{
			count: binary.BigEndian.Uint32(p[8+i*8:]),
			delta: binary.BigEndian.Uint32(p[12+i*8:]),
		}
	}
	return nil
}

func parseStss(p []byte, st *sampleTable) error {
	if len(p) < 8 {
		return fmt.Errorf("%w: truncated stss", ErrInvalidStructure)
	}
	n := binary.BigEndian.Uint32(p[4:8])
	if len(p) < 8+int(n)*4 {
		return fmt.Errorf("%w: truncated stss entries", ErrInvalidStructure)
	}
	st.keyframes = make([]uint32, n)
	prev := uint32(0)
	for i := range st.keyframes {
		v := binary.BigEndian.Uint32(p[8+i*4:])
		if i > 0 && v <= prev {
			return fmt.Errorf("%w: stss entries not strictly increasing", ErrInvalidStructure)
		}
		prev = v
		st.keyframes[i] = v
	}
	return nil
}

func parseStsc(p []byte, st *sampleTable) error {
	if len(p) < 8 {
		return fmt.Errorf("%w: truncated stsc", ErrInvalidStructure)
	}
	n := binary.BigEndian.Uint32(p[4:8])
	if len(p) < 8+int(n)*12 {
		return fmt.Errorf("%w: truncated stsc entries", ErrInvalidStructure)
	}
	st.stsc = make([]stscEntry, n)
	prev := uint32(0)
	for i := range st.stsc {
		fc := binary.BigEndian.Uint32(p[8+i*12:])
		spc := binary.BigEndian.Uint32(p[12+i*12:])
		if fc == 0 || (i > 0 && fc <= prev) {
			return fmt.Errorf("%w: stsc first_chunk not increasing", ErrInvalidStructure)
		}
		prev = fc
		st.stsc[i] = stscEntry{firstChunk: fc, samplesPerChunk: spc}
	}
	return nil
}

func parseStsz(p []byte, st *sampleTable) error {
	if len(p) < 12 {
		return fmt.Errorf("%w: truncated stsz", ErrInvalidStructure)
	}
	uniform := binary.BigEndian.Uint32(p[4:8])
	count := binary.BigEndian.Uint32(p[8:12])
	st.sampleCount = uint64(count)
	if uniform != 0 {
		st.uniformSize = uniform
		return nil
	}
	if uint64(len(p)) < 12+uint64(count)*4 {
		return fmt.Errorf("%w: truncated stsz entries", ErrInvalidStructure)
	}
	st.sizePrefix = make([]uint64, uint64(count)+1)
	var cum uint64
	for i := uint32(0); i < count; i++ {
		cum += uint64(binary.BigEndian.Uint32(p[12+i*4:]))
		st.sizePrefix[i+1] = cum
	}
	return nil
}

func parseStz2(p []byte, st *sampleTable) error {
	if len(p) < 8 {
		return fmt.Errorf("%w: truncated stz2", ErrInvalidStructure)
	}
	fieldSize := p[7]
	count := binary.BigEndian.Uint32(p[8:12])
	st.sampleCount = uint64(count)
	data := p[12:]
	st.sizePrefix = make([]uint64, uint64(count)+1)
	var cum uint64
	switch fieldSize {
	case 4:
		if uint64(len(data))*2 < uint64(count) {
			return fmt.Errorf("%w: truncated stz2 4-bit entries", ErrInvalidStructure)
		}
		for i := uint32(0); i < count; i++ {
			b := data[i/2]
			var v uint32
			if i%2 == 0 {
				v = uint32(b >> 4)
			} else {
				v = uint32(b & 0x0f)
			}
			cum += uint64(v)
			st.sizePrefix[i+1] = cum
		}
	case 8:
		if uint64(len(data)) < uint64(count) {
			return fmt.Errorf("%w: truncated stz2 8-bit entries", ErrInvalidStructure)
		}
		for i := uint32(0); i < count; i++ {
			cum += uint64(data[i])
			st.sizePrefix[i+1] = cum
		}
	case 16:
		if uint64(len(data)) < uint64(count)*2 {
			return fmt.Errorf("%w: truncated stz2 16-bit entries", ErrInvalidStructure)
		}
		for i := uint32(0); i < count; i++ {
			cum += uint64(binary.BigEndian.Uint16(data[i*2:]))
			st.sizePrefix[i+1] = cum
		}
	default:
		return fmt.Errorf("%w: unsupported stz2 field size %d", ErrInvalidStructure, fieldSize)
	}
	return nil
}

func parseStco(p []byte, st *sampleTable) error {
	if len(p) < 8 {
		return fmt.Errorf("%w: truncated stco", ErrInvalidStructure)
	}
	n := binary.BigEndian.Uint32(p[4:8])
	if uint64(len(p)) < 8+uint64(n)*4 {
		return fmt.Errorf("%w: truncated stco entries", ErrInvalidStructure)
	}
	st.chunkOffsets = make([]int64, n)
	for i := range st.chunkOffsets {
		st.chunkOffsets[i] = int64(binary.BigEndian.Uint32(p[8+i*4:]))
	}
	return nil
}

func parseCo64(p []byte, st *sampleTable) error {
	if len(p) < 8 {
		return fmt.Errorf("%w: truncated co64", ErrInvalidStructure)
	}
	n := binary.BigEndian.Uint32(p[4:8])
	if uint64(len(p)) < 8+uint64(n)*8 {
		return fmt.Errorf("%w: truncated co64 entries", ErrInvalidStructure)
	}
	st.chunkOffsets = make([]int64, n)
	for i := range st.chunkOffsets {
		v := binary.BigEndian.Uint64(p[8+i*8:])
		if v > uint64(1<<63-1) {
			return fmt.Errorf("%w: co64 offset overflows int64", ErrInvalidStructure)
		}
		st.chunkOffsets[i] = int64(v)
	}
	return nil
}
