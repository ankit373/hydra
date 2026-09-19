// SPDX-License-Identifier: MIT

package util

import (
	"encoding/base64"
	"encoding/binary"
	"math"
)

// EncodeVec and DecodeVec are the one on-disk form for an embedding, shared by
// the answer cache and the eval-set corpus. Two codecs would be two formats the
// moment either changed, and a vector written by one reader and read by the
// other is exactly what both stores are for.
func EncodeVec(vec []float32) string {
	if len(vec) == 0 {
		return ""
	}
	buf := make([]byte, 4*len(vec))
	for i, f := range vec {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(f))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// DecodeVec returns nil for anything it cannot read. A partly-decoded vector is
// a different point in space, so there is no useful half answer.
func DecodeVec(s string) []float32 {
	if s == "" {
		return nil
	}
	buf, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(buf)%4 != 0 {
		return nil
	}
	out := make([]float32, len(buf)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[4*i:]))
	}
	return out
}
