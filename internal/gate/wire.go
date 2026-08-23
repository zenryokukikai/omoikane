// Package gate implements the gateway (client) side of the opencrab
// external gate protocol 2 wire contract (issue #104, slice G1). The
// authoritative grammar is opencrab docs/design/external-gate.md §5
// (framing, message union, field grammar, violation table) and §6
// (connection state machine).
//
// Role split (spec §1): a gateway does external I/O and protocol
// translation only. This package therefore knows nothing about
// omoikane's store, routing, or policy — it turns Go calls into LF-JSON
// frames on a Unix socket and incoming frames into handler callbacks.
//
// This file owns the transport framing: one UTF-8 JSON object per line,
// LF-terminated, at most 1,048,576 bytes including the LF, duplicate
// members rejected anywhere in the document (Go's encoding/json accepts
// duplicates by default, so rejection is a manual token walk).
package gate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"unicode/utf8"
)

// MaxFrameBytes is the spec §5 hard cap for one frame, including the
// trailing LF.
const MaxFrameBytes = 1048576

var (
	// ErrFrameTooLarge reports a frame over MaxFrameBytes, in either
	// direction. An oversized incoming frame closes the connection
	// (violation table: too_large → close).
	ErrFrameTooLarge = errors.New("gate: frame exceeds 1048576 bytes including LF")

	errInvalidUTF8     = errors.New("gate: frame is not valid UTF-8")
	errNotObject       = errors.New("gate: frame is not a single JSON object")
	errTrailingData    = errors.New("gate: frame has trailing data after the JSON object")
	errDuplicateMember = errors.New("gate: duplicate object member in frame")
)

// frameReader yields LF-delimited frames from the socket. It enforces
// the size cap while reading so an oversized line fails without
// buffering more than MaxFrameBytes.
type frameReader struct {
	br *bufio.Reader
}

func newFrameReader(r io.Reader) *frameReader {
	return &frameReader{br: bufio.NewReaderSize(r, 64*1024)}
}

// next returns the next frame without its trailing LF. A line whose
// length including the LF exceeds MaxFrameBytes returns
// ErrFrameTooLarge.
func (r *frameReader) next() ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.br.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > MaxFrameBytes {
			return nil, ErrFrameTooLarge
		}
		switch err {
		case nil:
			return line[:len(line)-1], nil
		case bufio.ErrBufferFull:
			continue
		default:
			if err == io.EOF && len(line) > 0 {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
	}
}

// frameWriter serializes one value per line. The mutex makes a frame an
// atomic unit: concurrent requests and handler responses never
// interleave bytes.
type frameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// writeFrame marshals v, enforces the size cap, and writes it with the
// trailing LF in a single Write call.
func (w *frameWriter) writeFrame(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("gate: marshal frame: %w", err)
	}
	if len(b)+1 > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.w.Write(b)
	return err
}

// validateFrameShape enforces the transport-level rules that apply to
// every incoming frame before any typed decode: valid UTF-8, exactly
// one JSON object, and no duplicate members at any nesting depth
// (free-JSON regions like effect.payload included — spec §5 states the
// duplicate rule unconditionally for the frame).
func validateFrameShape(data []byte) error {
	if !utf8.Valid(data) {
		return errInvalidUTF8
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("gate: malformed frame: %w", err)
	}
	if tok != json.Delim('{') {
		return errNotObject
	}
	if err := walkObjectMembers(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errTrailingData
	}
	return nil
}

// walkObjectMembers consumes an object body (opening '{' already read),
// rejecting duplicate keys, recursing into nested values.
func walkObjectMembers(dec *json.Decoder) error {
	seen := make(map[string]struct{})
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("gate: malformed frame: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return errNotObject
		}
		if _, dup := seen[key]; dup {
			return errDuplicateMember
		}
		seen[key] = struct{}{}
		if err := walkValue(dec); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil { // consume '}'
		return fmt.Errorf("gate: malformed frame: %w", err)
	}
	return nil
}

// walkValue consumes one JSON value, recursing into containers.
func walkValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("gate: malformed frame: %w", err)
	}
	switch tok {
	case json.Delim('{'):
		return walkObjectMembers(dec)
	case json.Delim('['):
		for dec.More() {
			if err := walkValue(dec); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil { // consume ']'
			return fmt.Errorf("gate: malformed frame: %w", err)
		}
	}
	return nil
}

// decodeStrictBody decodes data into v rejecting unknown members
// (recursively, per typed struct fields) and trailing data. The caller
// must already have run validateFrameShape over the enclosing frame, so
// UTF-8 and duplicate members are settled by the time this runs.
func decodeStrictBody(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("gate: strict decode: %w", err)
	}
	if dec.More() {
		return errTrailingData
	}
	return nil
}
