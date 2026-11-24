package search

import (
	"encoding/binary"
	"math"
	"slices"
)

// How a vector is stored and compared (ADR 0005, choice G3). Every vector is kept unit-length, so a dot product is
// the cosine, as float32 for the exact score and as int8 for the first pass: a quarter of the bytes to scan, close
// enough to pick the few hundred candidates the float32 copies then rank exactly.

// Normalize returns the unit-length form of v; a zero vector stays zero.
func Normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	n = math.Sqrt(n)
	if n == 0 {
		n = 1
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out
}

// quantize scales each component of a unit-length vector (within [-1, 1]) to [-127, 127]. Rounding is half up, as
// Math.round, so the bytes stored by earlier versions stay the same.
func quantize(unit []float32) []int8 {
	q := make([]int8, len(unit))
	for i, x := range unit {
		q[i] = int8(math.Floor(math.Max(-1, math.Min(1, float64(x)))*127 + 0.5))
	}
	return q
}

// Float32Bytes is the stored form of a vector.
func Float32Bytes(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func i8Bytes(v []int8) []byte {
	b := make([]byte, len(v))
	for i, x := range v {
		b[i] = byte(x)
	}
	return b
}

// dotF32 is the dot product of a float32 vector and one stored as bytes, over the shorter of the two.
func dotF32(a []float32, b []byte) float64 {
	n := min(len(a), len(b)/4)
	var s float64
	for i := range n {
		s += float64(a[i]) * float64(math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:])))
	}
	return s
}

// dotI8 is the dot product of an int8 vector and one stored as bytes, over the shorter of the two.
func dotI8(a []int8, b []byte) int {
	n := min(len(a), len(b))
	var s int
	for i := range n {
		s += int(a[i]) * int(int8(b[i]))
	}
	return s
}

type scored[T any] struct {
	item  T
	score float64
}

// topK keeps the k best items added to it, best first, without sorting all of them.
type topK[T any] struct {
	k    int
	best []scored[T]
}

func newTop[T any](k int) *topK[T] { return &topK[T]{k: k} }

// add offers an item. A sorted slice of at most k: inserting is O(k), fine for the few hundred kept here.
func (t *topK[T]) add(item T, score float64) {
	if t.k < 1 || len(t.best) == t.k && score <= t.best[t.k-1].score {
		return
	}
	i := len(t.best)
	for i > 0 && t.best[i-1].score < score {
		i--
	}
	t.best = slices.Insert(t.best, i, scored[T]{item, score})
	if len(t.best) > t.k {
		t.best = t.best[:t.k]
	}
}

// Normalize, Dot, Float32s and Float32Bytes serve the packages that compare stored vectors (the memory's nearness of meaning).

// Dot is the dot product of two vectors over the shorter of the two, summed in float64.
func Dot(a, b []float32) float64 {
	var s float64
	for i := range min(len(a), len(b)) {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// Float32s reads a vector as the index stores it: little-endian float32.
func Float32s(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}
