// SPDX-License-Identifier: MIT

package executor

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/ankit373/hydra/internal/util"
)

// AWS event-stream framing, which Bedrock's ConverseStream answers with instead
// of SSE: a 12-byte prelude of two lengths and their checksum, then headers, a
// payload, and a checksum over everything before it (#876).
const (
	eventPreludeBytes = 12
	eventFrameTrailer = 4
	// The format's own ceiling. A length past it is a misread stream rather
	// than a large message, and allocating on it would be the bug.
	maxEventFrameBytes = 16 * 1024 * 1024
)

// eventFrame is one decoded message. Only string headers are kept: they are
// what routes a frame (`:event-type`, `:message-type`), and the numeric types
// still have to be skipped correctly or the rest of the headers misparse.
type eventFrame struct {
	Headers map[string]string
	Payload []byte
}

// scanEventStream decodes frames until the body ends, calling onFrame for each.
//
// The checksums are verified rather than skipped: a truncated frame otherwise
// decodes as a shorter answer, which is exactly the silent failure this series
// of dialects exists to prevent.
func scanEventStream(r io.Reader, onFrame func(eventFrame) error) error {
	br := bufio.NewReader(io.LimitReader(r, int64(util.DefaultMaxBytes)+1))
	prelude := make([]byte, eventPreludeBytes)
	for {
		if _, err := io.ReadFull(br, prelude); err != nil {
			if errors.Is(err, io.EOF) {
				return nil // a clean end between frames
			}
			return fmt.Errorf("event stream: read prelude: %w", err)
		}
		total := binary.BigEndian.Uint32(prelude[0:4])
		headersLen := binary.BigEndian.Uint32(prelude[4:8])
		if want := binary.BigEndian.Uint32(prelude[8:12]); want != crc32.ChecksumIEEE(prelude[:8]) {
			return errors.New("event stream: prelude failed its checksum")
		}
		if uint64(total) > maxEventFrameBytes || uint64(total) < uint64(headersLen)+eventPreludeBytes+eventFrameTrailer {
			return fmt.Errorf("event stream: frame length %d does not hold %d header bytes", total, headersLen)
		}

		rest := make([]byte, total-eventPreludeBytes)
		if _, err := io.ReadFull(br, rest); err != nil {
			return fmt.Errorf("event stream: truncated frame: %w", err)
		}
		body := rest[:len(rest)-eventFrameTrailer]
		want := binary.BigEndian.Uint32(rest[len(rest)-eventFrameTrailer:])
		if crc32.Update(crc32.ChecksumIEEE(prelude), crc32.IEEETable, body) != want {
			return errors.New("event stream: frame failed its checksum")
		}

		headers, err := parseEventHeaders(body[:headersLen])
		if err != nil {
			return fmt.Errorf("event stream: %w", err)
		}
		if err := onFrame(eventFrame{Headers: headers, Payload: body[headersLen:]}); err != nil {
			return err
		}
	}
}

// parseEventHeaders reads the header block. Values of a type this does not need
// are skipped by their declared width, because a type whose width is unknown
// desynchronises every header after it.
func parseEventHeaders(b []byte) (map[string]string, error) {
	headers := make(map[string]string)
	for len(b) > 0 {
		nameLen := int(b[0])
		b = b[1:]
		if len(b) < nameLen+1 {
			return nil, errors.New("truncated header name")
		}
		name := string(b[:nameLen])
		valueType := b[nameLen]
		b = b[nameLen+1:]

		if valueType == eventHeaderString || valueType == eventHeaderBytes {
			if len(b) < 2 {
				return nil, errors.New("truncated header length")
			}
			n := int(binary.BigEndian.Uint16(b))
			b = b[2:]
			if len(b) < n {
				return nil, errors.New("truncated header value")
			}
			if valueType == eventHeaderString {
				headers[name] = string(b[:n])
			}
			b = b[n:]
			continue
		}
		width, ok := eventHeaderWidths[valueType]
		if !ok {
			return nil, fmt.Errorf("header %q has unknown value type %d", name, valueType)
		}
		if len(b) < width {
			return nil, errors.New("truncated header value")
		}
		b = b[width:]
	}
	return headers, nil
}

const (
	eventHeaderBytes  byte = 6
	eventHeaderString byte = 7
)

// Fixed widths for the value types this does not read: bool true/false, byte,
// short, integer, long, timestamp, uuid.
var eventHeaderWidths = map[byte]int{0: 0, 1: 0, 2: 1, 3: 2, 4: 4, 5: 8, 8: 8, 9: 16}
