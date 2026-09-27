package mp4post

import "errors"

var (
	// ErrFragmented indicates the file is a fragmented MP4, which is unsupported.
	ErrFragmented = errors.New("mp4post: fragmented MP4 is not supported")
	// ErrCompressedMoov indicates the moov index is compressed (cmov), which is unsupported.
	ErrCompressedMoov = errors.New("mp4post: compressed moov (cmov) is not supported")
	// ErrInvalidStructure indicates the file is malformed, truncated, or not an MP4.
	ErrInvalidStructure = errors.New("mp4post: invalid or truncated MP4 structure")
	// ErrNoMoov indicates the file has no moov box.
	ErrNoMoov = errors.New("mp4post: no moov box found")
	// ErrTrackNotFound indicates the requested track ID does not exist.
	ErrTrackNotFound = errors.New("mp4post: track not found")
	// ErrSampleNotFound indicates the requested sample number is out of range.
	ErrSampleNotFound = errors.New("mp4post: sample not found")
	// ErrNoKeyframe indicates no keyframe exists at or before the requested time.
	ErrNoKeyframe = errors.New("mp4post: no keyframe at or before the given time")
	// ErrAlreadyFastStart indicates the moov index already precedes the first mdat.
	ErrAlreadyFastStart = errors.New("mp4post: moov already precedes first mdat, nothing to do")
)
