package mp4post

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FastStart rewrites the MP4 at srcPath so the moov box sits immediately
// before the first mdat box, writing the result to dstPath. All other
// top-level boxes keep their content and relative order.
//
// If the index is already before the first mdat, it returns
// ErrAlreadyFastStart and writes nothing. On any error the source file is
// untouched and no partial output is left at dstPath.
//
// Memory usage is bounded (a small copy buffer plus the moov box itself);
// runtime is on the order of one sequential copy of the file.
func FastStart(srcPath, dstPath string) error {
	if srcPath == dstPath {
		return fmt.Errorf("mp4post: source and destination must differ")
	}
	src, err := Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	if src.MoovBeforeFirstMdat() {
		return ErrAlreadyFastStart
	}

	moov := src.boxes[src.moovIdx]
	insertion := src.boxes[src.firstMdat].headerOff
	// Only boxes between the insertion point and moov's old position
	// move; boxes after moov keep their absolute offsets.
	shiftEnd := moov.headerOff

	moovPayload, err := readPayload(src.f, moov)
	if err != nil {
		return err
	}

	// Converge on the moov size growth caused by stco->co64 upgrades:
	// the shift depends on the new moov size, which depends on whether
	// any chunk offset crosses 4 GiB because of the shift.
	oldMoovSize := moov.headerSize + moov.payloadSize
	var growth int64
	var newMoov []byte
	for {
		shift := oldMoovSize + growth
		newMoov, err = rewriteMoov(moovPayload, src.tracks, insertion, shiftEnd, shift)
		if err != nil {
			return err
		}
		newGrowth := int64(len(newMoov)) + moovHeaderSize(int64(len(newMoov))) - oldMoovSize
		if newGrowth == growth {
			break
		}
		growth = newGrowth
	}

	// Write to a temp file in the destination directory, then rename, so
	// a failure never leaves a partial file at dstPath.
	dir := filepath.Dir(dstPath)
	tmp, err := os.CreateTemp(dir, ".mp4post-faststart-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(tmpName)
		}
	}()

	buf := make([]byte, 1<<20)
	writeBox := func(h boxHeader) error {
		sr := io.NewSectionReader(src.f, h.headerOff, h.end()-h.headerOff)
		_, err := io.CopyBuffer(tmp, sr, buf)
		return err
	}
	for _, b := range src.boxes {
		if b.headerOff == insertion {
			if err := writeMoovBox(tmp, newMoov); err != nil {
				return err
			}
		}
		if string(b.typ[:]) == "moov" && b.headerOff == moov.headerOff {
			continue
		}
		if err := writeBox(b); err != nil {
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		return err
	}
	ok = true
	return nil
}

// moovHeaderSize returns 8 or 16 depending on the payload size.
func moovHeaderSize(payloadSize int64) int64 {
	if payloadSize+8 <= 0xFFFFFFFF {
		return 8
	}
	return 16
}

// writeMoovBox writes the moov box header plus payload.
func writeMoovBox(w io.Writer, payload []byte) error {
	var hdr [16]byte
	n := moovHeaderSize(int64(len(payload)))
	if n == 8 {
		binary.BigEndian.PutUint32(hdr[0:4], uint32(len(payload))+8)
	} else {
		binary.BigEndian.PutUint32(hdr[0:4], 1)
		binary.BigEndian.PutUint64(hdr[8:16], uint64(len(payload))+16)
	}
	copy(hdr[4:8], "moov")
	if _, err := w.Write(hdr[:n]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// rawBox is a box inside a container payload, keeping its original header
// bytes so unchanged boxes can be copied verbatim.
type rawBox struct {
	typ      [4]byte
	payload  []byte
	raw      []byte // original header+payload bytes
	sizeZero bool   // size field was 0 (extends to end of parent)
}

func iterRaw(payload []byte, fn func(b rawBox) error) error {
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
		sizeZero := false
		switch size32 {
		case 0:
			size = len(payload) - off
			sizeZero = true
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
		if err := fn(rawBox{
			typ:      typ,
			payload:  payload[off+hdr : off+size],
			raw:      payload[off : off+size],
			sizeZero: sizeZero,
		}); err != nil {
			return err
		}
		off += size
	}
	return nil
}

// emitBox re-encodes a box whose content changed, preserving a size==0
// header if the original had one.
func emitBox(typ [4]byte, payload []byte, sizeZero bool) []byte {
	if sizeZero {
		out := make([]byte, 8+len(payload))
		copy(out[4:8], typ[:])
		copy(out[8:], payload)
		return out
	}
	if len(payload)+8 <= 0xFFFFFFFF {
		out := make([]byte, 8+len(payload))
		binary.BigEndian.PutUint32(out[0:4], uint32(len(payload))+8)
		copy(out[4:8], typ[:])
		copy(out[8:], payload)
		return out
	}
	out := make([]byte, 16+len(payload))
	binary.BigEndian.PutUint32(out[0:4], 1)
	copy(out[4:8], typ[:])
	binary.BigEndian.PutUint64(out[8:16], uint64(len(payload))+16)
	copy(out[16:], payload)
	return out
}

// rewriteMoov returns the moov payload with every stco/co64 chunk offset
// adjusted: offsets at or after the insertion point move right by shift.
// stco boxes whose new offsets exceed 32 bits are upgraded to co64.
func rewriteMoov(payload []byte, tracks []*track, insertion, shiftEnd, shift int64) ([]byte, error) {
	trakIdx := 0
	var rewrite func(p []byte, depth, curTrack int) ([]byte, bool, error)
	rewrite = func(p []byte, depth, curTrack int) ([]byte, bool, error) {
		changed := false
		var out []byte
		err := iterRaw(p, func(b rawBox) error {
			typ := string(b.typ[:])
			switch {
			case depth < 4 && (typ == "trak" || typ == "mdia" || typ == "minf" || typ == "stbl"):
				if typ == "trak" {
					curTrack = trakIdx
					trakIdx++
				}
				np, ch, err := rewrite(b.payload, depth+1, curTrack)
				if err != nil {
					return err
				}
				if ch {
					changed = true
					out = append(out, emitBox(b.typ, np, b.sizeZero)...)
				} else {
					out = append(out, b.raw...)
				}
			case depth == 4 && (typ == "stco" || typ == "co64"):
				if curTrack < 0 || curTrack >= len(tracks) {
					return fmt.Errorf("%w: stco/co64 outside trak", ErrInvalidStructure)
				}
				nb, err := rewriteOffsets(b, tracks[curTrack].table.chunkOffsets, insertion, shiftEnd, shift)
				if err != nil {
					return err
				}
				changed = true
				out = append(out, nb...)
			default:
				out = append(out, b.raw...)
			}
			return nil
		})
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return p, false, nil
		}
		return out, true, nil
	}
	newPayload, _, err := rewrite(payload, 0, -1)
	return newPayload, err
}

// rewriteOffsets re-encodes one stco/co64 box with shifted offsets,
// upgrading stco to co64 when any offset no longer fits in 32 bits.
func rewriteOffsets(b rawBox, offsets []int64, insertion, shiftEnd, shift int64) ([]byte, error) {
	if len(b.payload) < 8 {
		return nil, fmt.Errorf("%w: truncated %.4s", ErrInvalidStructure, b.typ)
	}
	count := int(binary.BigEndian.Uint32(b.payload[4:8]))
	if count != len(offsets) {
		return nil, fmt.Errorf("%w: %.4s count %d != parsed %d", ErrInvalidStructure, b.typ, count, len(offsets))
	}
	isCo64 := string(b.typ[:]) == "co64"
	needCo64 := isCo64
	newOff := make([]int64, count)
	for i, o := range offsets {
		if o >= insertion && o < shiftEnd {
			o += shift
		}
		newOff[i] = o
		if o > 0xFFFFFFFF {
			needCo64 = true
		}
	}
	entrySize := 4
	if needCo64 {
		entrySize = 8
	}
	payload := make([]byte, 8+count*entrySize)
	copy(payload[0:4], b.payload[0:4]) // version + flags
	binary.BigEndian.PutUint32(payload[4:8], uint32(count))
	for i, o := range newOff {
		if needCo64 {
			binary.BigEndian.PutUint64(payload[8+i*8:], uint64(o))
		} else {
			binary.BigEndian.PutUint32(payload[8+i*4:], uint32(o))
		}
	}
	typ := b.typ
	if needCo64 && !isCo64 {
		typ = [4]byte{'c', 'o', '6', '4'}
	}
	return emitBox(typ, payload, false), nil
}
