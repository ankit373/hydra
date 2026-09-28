// SPDX-License-Identifier: MIT

package executor

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"strings"
	"testing"
	"testing/iotest"
)

// encodeEventFrame builds one message the way the format specifies, so the
// decoder is exercised against real framing rather than a simplification of it.
func encodeEventFrame(headers map[string]string, payload []byte) []byte {
	var hb bytes.Buffer
	for k, v := range headers {
		hb.WriteByte(byte(len(k)))
		hb.WriteString(k)
		hb.WriteByte(eventHeaderString)
		_ = binary.Write(&hb, binary.BigEndian, uint16(len(v)))
		hb.WriteString(v)
	}
	return frameFrom(hb.Bytes(), payload)
}

func frameFrom(headers, payload []byte) []byte {
	total := uint32(eventPreludeBytes + len(headers) + len(payload) + eventFrameTrailer)
	prelude := make([]byte, eventPreludeBytes)
	binary.BigEndian.PutUint32(prelude[0:4], total)
	binary.BigEndian.PutUint32(prelude[4:8], uint32(len(headers)))
	binary.BigEndian.PutUint32(prelude[8:12], crc32.ChecksumIEEE(prelude[:8]))

	msg := append(append(append([]byte{}, prelude...), headers...), payload...)
	trailer := make([]byte, eventFrameTrailer)
	binary.BigEndian.PutUint32(trailer, crc32.ChecksumIEEE(msg))
	return append(msg, trailer...)
}

func collectFrames(t *testing.T, b []byte) ([]eventFrame, error) {
	t.Helper()
	var got []eventFrame
	err := scanEventStream(bytes.NewReader(b), func(f eventFrame) error {
		got = append(got, f)
		return nil
	})
	return got, err
}

func TestEventStream_DecodesFramesAndTheirHeaders(t *testing.T) {
	var b []byte
	b = append(b, encodeEventFrame(map[string]string{":event-type": "messageStart"}, []byte(`{"role":"assistant"}`))...)
	b = append(b, encodeEventFrame(map[string]string{":event-type": "contentBlockDelta"}, []byte(`{"delta":{"text":"hi"}}`))...)

	got, err := collectFrames(t, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("decoded %d frames, want 2", len(got))
	}
	if got[0].Headers[":event-type"] != "messageStart" {
		t.Errorf("first header = %v", got[0].Headers)
	}
	if string(got[1].Payload) != `{"delta":{"text":"hi"}}` {
		t.Errorf("second payload = %q", got[1].Payload)
	}
}

// A frame arrives in whatever pieces the network hands over, never neatly.
func TestEventStream_DecodesAFrameSplitAcrossReads(t *testing.T) {
	b := encodeEventFrame(map[string]string{":event-type": "contentBlockDelta"}, []byte(`{"delta":{"text":"split"}}`))

	var got []eventFrame
	err := scanEventStream(iotest.OneByteReader(bytes.NewReader(b)), func(f eventFrame) error {
		got = append(got, f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(string(got[0].Payload), "split") {
		t.Errorf("got %v, want the one frame whole", got)
	}
}

// Header values this does not read still have to be skipped by their width, or
// every header after one desynchronises.
func TestEventStream_SkipsNonStringHeaderValues(t *testing.T) {
	var hb bytes.Buffer
	writeStringHeader := func(k, v string) {
		hb.WriteByte(byte(len(k)))
		hb.WriteString(k)
		hb.WriteByte(eventHeaderString)
		_ = binary.Write(&hb, binary.BigEndian, uint16(len(v)))
		hb.WriteString(v)
	}
	writeStringHeader(":message-type", "event")
	// A timestamp, 8 bytes, of a kind this never reads.
	hb.WriteByte(byte(len(":date")))
	hb.WriteString(":date")
	hb.WriteByte(8)
	_ = binary.Write(&hb, binary.BigEndian, int64(1700000000))
	writeStringHeader(":event-type", "metadata")

	got, err := collectFrames(t, frameFrom(hb.Bytes(), []byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Headers[":event-type"] != "metadata" {
		t.Errorf("headers = %v, want the header after the timestamp to still parse", got[0].Headers)
	}
}

func TestEventStream_RejectsACorruptedFrame(t *testing.T) {
	good := encodeEventFrame(map[string]string{":event-type": "contentBlockDelta"}, []byte(`{"delta":{"text":"hi"}}`))

	cases := map[string]func([]byte) []byte{
		"payload edited under its checksum": func(b []byte) []byte {
			c := append([]byte{}, b...)
			c[len(c)-6] ^= 0xFF
			return c
		},
		"prelude length corrupted": func(b []byte) []byte {
			c := append([]byte{}, b...)
			c[3]++
			return c
		},
		"frame cut short": func(b []byte) []byte { return b[:len(b)-3] },
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := collectFrames(t, corrupt(good)); err == nil {
				t.Error("no error, so a truncated stream would read as a shorter answer")
			}
		})
	}
}

func TestEventStream_EmptyBodyIsNotAnError(t *testing.T) {
	got, err := collectFrames(t, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v; want no frames and no error", got, err)
	}
}
