package frame

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(Append(nil, OK, []byte(`{"a":1}`)))
	buf.Write(Append(nil, Ready, nil))

	kind, payload, err := Read(&buf, 1<<20)
	if err != nil || kind != OK || string(payload) != `{"a":1}` {
		t.Fatalf("got %d %q %v", kind, payload, err)
	}
	kind, payload, err = Read(&buf, 1<<20)
	if err != nil || kind != Ready || len(payload) != 0 {
		t.Fatalf("got %d %q %v", kind, payload, err)
	}
	if _, _, err := Read(&buf, 1<<20); err != io.EOF {
		t.Fatalf("want io.EOF at end of stream, got %v", err)
	}
}

func TestAppendRequest(t *testing.T) {
	kind, payload, err := Read(bytes.NewReader(AppendRequest(nil, "add", []byte(`{"a":1}`))), 1<<20)
	if err != nil || kind != Request {
		t.Fatalf("got %d %v", kind, err)
	}
	if n := int(payload[0]); string(payload[1:1+n]) != "add" || string(payload[1+n:]) != `{"a":1}` {
		t.Fatalf("bad payload %q", payload)
	}
}

func TestTooLarge(t *testing.T) {
	_, _, err := Read(bytes.NewReader(Append(nil, OK, make([]byte, 100))), 99)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestTruncated(t *testing.T) {
	f := Append(nil, OK, []byte("hello"))
	if _, _, err := Read(bytes.NewReader(f[:len(f)-2]), 1<<20); err != io.ErrUnexpectedEOF {
		t.Fatalf("want io.ErrUnexpectedEOF, got %v", err)
	}
	if _, _, err := Read(bytes.NewReader(f[:3]), 1<<20); err != io.ErrUnexpectedEOF {
		t.Fatalf("want io.ErrUnexpectedEOF for short header, got %v", err)
	}
}
