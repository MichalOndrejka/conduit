package rag

import (
	"math"
	"testing"
)

func TestPCA2DSeparatesClusters(t *testing.T) {
	// Two clusters far apart along one axis must separate along PC1.
	var vectors [][]float32
	for i := 0; i < 10; i++ {
		vectors = append(vectors, []float32{10, 0, float32(i) * 0.01})
		vectors = append(vectors, []float32{-10, 0, float32(i) * 0.01})
	}
	coords := PCA2D(vectors)
	if len(coords) != 20 {
		t.Fatalf("got %d coords", len(coords))
	}
	for i := 0; i < len(coords); i += 2 {
		if math.Signbit(coords[i][0]) == math.Signbit(coords[i+1][0]) {
			t.Fatalf("clusters not separated on PC1: %v vs %v", coords[i], coords[i+1])
		}
	}
}

func TestPCA2DEdgeCases(t *testing.T) {
	if got := PCA2D(nil); len(got) != 0 {
		t.Error("nil input should give empty output")
	}
	if got := PCA2D([][]float32{{1, 2, 3}}); len(got) != 1 {
		t.Error("single vector should give one (origin) point")
	}
	// All-identical vectors must not produce NaNs.
	same := [][]float32{{1, 1}, {1, 1}, {1, 1}}
	for _, c := range PCA2D(same) {
		if math.IsNaN(c[0]) || math.IsNaN(c[1]) {
			t.Error("NaN coordinates for degenerate input")
		}
	}
}

func TestPCAComponentsOrthogonal(t *testing.T) {
	// Spread along two axes with different variance; projections onto PC1
	// must carry more variance than PC2.
	var vectors [][]float32
	for i := -5; i <= 5; i++ {
		vectors = append(vectors, []float32{float32(i) * 3, float32(i % 2), 0})
	}
	coords := PCA2D(vectors)
	var var1, var2 float64
	for _, c := range coords {
		var1 += c[0] * c[0]
		var2 += c[1] * c[1]
	}
	if var1 <= var2 {
		t.Errorf("PC1 variance (%f) should exceed PC2 variance (%f)", var1, var2)
	}
}
