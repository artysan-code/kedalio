package search

import (
	"math"
	"slices"
	"testing"
)

func dotUnit(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

func TestNormalizeUnitLengthZeroStaysZero(t *testing.T) {
	n := Normalize([]float32{3, 4})
	if math.Abs(dotUnit(n, n)-1) > 1e-6 {
		t.Fatalf("norm² = %v", dotUnit(n, n))
	}
	if z := Normalize([]float32{0, 0}); z[0] != 0 || z[1] != 0 {
		t.Fatalf("got %v", z)
	}
}

func TestInt8ScoreRanksLikeFloat32(t *testing.T) {
	q, a, b := Normalize([]float32{1, 2, 3, 4}), Normalize([]float32{1, 2, 3, 5}), Normalize([]float32{4, 3, 2, 1})
	if dotUnit(q, a) <= dotUnit(q, b) {
		t.Fatal("float ranking")
	}
	qa, qb := dotI8(quantize(q), i8Bytes(quantize(a))), dotI8(quantize(q), i8Bytes(quantize(b)))
	if qa <= qb {
		t.Fatal("int8 ranking")
	}
	if math.Abs(float64(qa)/127/127-dotUnit(q, a)) > 0.02 {
		t.Fatalf("int8 %v vs float %v", float64(qa)/127/127, dotUnit(q, a))
	}
}

func TestQuantizeRoundsHalfUp(t *testing.T) {
	if got := quantize([]float32{0.5, -0.5, 2, -2}); !slices.Equal(got, []int8{64, -63, 127, -127}) {
		t.Fatalf("got %v", got)
	}
}

func TestStoredBytesReadBack(t *testing.T) {
	v := Normalize([]float32{1, 2, 3})
	if got := dotF32(v, Float32Bytes(v)); math.Abs(got-dotUnit(v, v)) > 1e-9 {
		t.Fatalf("got %v", got)
	}
	if got := dotI8(quantize(v), i8Bytes(quantize(v))); got < 127*127/2 {
		t.Fatalf("got %v", got)
	}
}

func TestTopKeepsTheKBestBestFirst(t *testing.T) {
	top := newTop[int](3)
	for _, x := range []int{5, 1, 9, 3, 7} {
		top.add(x, float64(x))
	}
	var got []int
	for _, s := range top.best {
		got = append(got, s.item)
	}
	if !slices.Equal(got, []int{9, 7, 5}) {
		t.Fatalf("got %v", got)
	}
	if len(newTop[int](3).best) != 0 {
		t.Fatal("empty")
	}
}
