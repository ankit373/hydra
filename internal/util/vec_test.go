// SPDX-License-Identifier: MIT

package util

import "testing"

// Encoding is little-endian float32 both ways, so a vector written on one run
// is readable on the next, and by the other store.
func TestVecRoundTrip(t *testing.T) {
	in := []float32{0, 1, -1, 0.5, 3.25}
	out := DecodeVec(EncodeVec(in))
	if len(out) != len(in) {
		t.Fatalf("got %d floats, want %d", len(out), len(in))
	}
	for i := range in {
		if in[i] != out[i] {
			t.Errorf("element %d: %v != %v", i, out[i], in[i])
		}
	}
	if EncodeVec(nil) != "" || DecodeVec("") != nil {
		t.Error("an absent vector did not round-trip as absent")
	}
}

// A vector that cannot be read whole is not a vector: a truncated one decodes
// to a different point, which reads as a real answer.
func TestDecodeVec_RefusesWhatItCannotRead(t *testing.T) {
	if DecodeVec("not base64 at all!") != nil {
		t.Error("garbage decoded to a vector")
	}
	// Valid base64, but not a whole number of float32s.
	if DecodeVec(EncodeVec([]float32{1, 2})[:6]) != nil {
		t.Error("a truncated vector decoded")
	}
}
