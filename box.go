package mp4post

import (
	"encoding/binary"
	"fmt"
	"io"
)

// boxHeader describes one box: its type, the file offset of its header,
// the offset of its payload, and its payload size.
type boxHeader struct {
	typ         [4]byte
	headerOff   int64 // offset of the box header (size field)
	payloadOff  int64 // offset of the box payload
	payloadSize int64 // payload size in bytes
	headerSize  int64 // 8 or 16
}

// end returns the offset just past this box.
func (h boxHeader) end() int64 { return h.payloadOff + h.payloadSize }

// readBoxHeader reads one box header at offset off. A 32-bit size of 0
// means the box extends to end of file. A 32-bit size of 1 means the real
// size is the following 64-bit value.
func readBoxHeader(r io.ReaderAt, off int64, fileSize int64) (boxHeader, error) {
	var h boxHeader
	if off < 0 || off+8 > fileSize {
		return h, fmt.Errorf("%w: box header at %d out of bounds (file size %d)", ErrInvalidStructure, off, fileSize)
	}
	var buf [16]byte
	if _, err := r.ReadAt(buf[:8], off); err != nil {
		return h, fmt.Errorf("%w: %v", ErrInvalidStructure, err)
	}
	size32 := binary.BigEndian.Uint32(buf[0:4])
	copy(h.typ[:], buf[4:8])
	h.headerOff = off
	h.headerSize = 8
	var size int64
	switch size32 {
	case 0:
		size = fileSize - off
	case 1:
		if off+16 > fileSize {
			return h, fmt.Errorf("%w: truncated largesize box at %d", ErrInvalidStructure, off)
		}
		if _, err := r.ReadAt(buf[8:16], off+8); err != nil {
			return h, fmt.Errorf("%w: %v", ErrInvalidStructure, err)
		}
		size64 := binary.BigEndian.Uint64(buf[8:16])
		if size64 > uint64(fileSize-off) {
			return h, fmt.Errorf("%w: box at %d size %d exceeds file", ErrInvalidStructure, off, size64)
		}
		size = int64(size64)
		h.headerSize = 16
	default:
		size = int64(size32)
	}
	if size < h.headerSize {
		return h, fmt.Errorf("%w: box at %d size %d smaller than header", ErrInvalidStructure, off, size)
	}
	if off+size > fileSize {
		return h, fmt.Errorf("%w: box at %d size %d exceeds file size %d", ErrInvalidStructure, off, size, fileSize)
	}
	h.payloadOff = off + h.headerSize
	h.payloadSize = size - h.headerSize
	return h, nil
}

// scanTopLevel returns headers of all top-level boxes in file order.
func scanTopLevel(r io.ReaderAt, fileSize int64) ([]boxHeader, error) {
	var out []boxHeader
	off := int64(0)
	for off < fileSize {
		h, err := readBoxHeader(r, off, fileSize)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
		off = h.end()
	}
	return out, nil
}

// readPayload reads the full payload of a box into memory.
// Callers must only use this for boxes known to be index-sized (moov and
// its children), never for mdat.
func readPayload(r io.ReaderAt, h boxHeader) ([]byte, error) {
	if h.payloadSize > 1<<31 {
		return nil, fmt.Errorf("%w: box %.4s payload too large (%d bytes)", ErrInvalidStructure, h.typ, h.payloadSize)
	}
	buf := make([]byte, h.payloadSize)
	if _, err := r.ReadAt(buf, h.payloadOff); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidStructure, err)
	}
	return buf, nil
}
