// Package frame implements the messages exchanged with Python workers.
//
// A frame is a 4-byte big-endian length, one kind byte, then the payload.
// The length counts the kind byte and the payload.
package frame

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Frame kinds.
const (
	Request byte = 1 // Go → Python: task name length, task name, JSON input
	Ready   byte = 2 // Python → Go: module imported, JSON worker info
	OK      byte = 3 // Python → Go: JSON output
	Error   byte = 4 // Python → Go: JSON error
	Retire  byte = 5 // Python → Go, before a reply: over its memory limit; JSON {"rss", "limit"}
)

// MaxTaskName is the longest task name a Request frame can carry.
const MaxTaskName = 255

// ErrTooLarge is returned when a frame's payload exceeds the reader's limit.
var ErrTooLarge = errors.New("frame too large")

// Append appends a frame of the given kind to dst.
func Append(dst []byte, kind byte, payload []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(1+len(payload)))
	dst = append(dst, kind)
	return append(dst, payload...)
}

// AppendRequest appends a Request frame to dst. task must be at most MaxTaskName bytes.
func AppendRequest(dst []byte, task string, input []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(2+len(task)+len(input)))
	dst = append(dst, Request, byte(len(task)))
	dst = append(dst, task...)
	return append(dst, input...)
}

// Read reads one frame. A payload larger than max bytes is rejected before it is read.
func Read(r io.Reader, max int) (kind byte, payload []byte, err error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint32(h[:4])) - 1
	if n < 0 {
		return 0, nil, errors.New("frame: zero length")
	}
	if n > max {
		return 0, nil, fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, n, max)
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return h[4], payload, nil
}
