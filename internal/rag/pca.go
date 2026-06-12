// PCA projection for the vector map — replaces scikit-learn/umap-learn from
// the Python app (the single heaviest dependency tree there). Top-2 principal
// components via power iteration with deflation: exact enough for a 2D
// scatter, ~80 lines, zero dependencies. UMAP is intentionally dropped (no Go
// implementation exists).
package rag

import "math"

// PCA2D projects vectors onto their top-2 principal components.
// Returns one [x, y] pair per input vector.
func PCA2D(vectors [][]float32) [][2]float64 {
	n := len(vectors)
	out := make([][2]float64, n)
	if n == 0 {
		return out
	}
	dim := len(vectors[0])
	if n == 1 || dim == 0 {
		return out // single point sits at the origin
	}

	// Center the data.
	mean := make([]float64, dim)
	for _, v := range vectors {
		for j, x := range v {
			mean[j] += float64(x)
		}
	}
	for j := range mean {
		mean[j] /= float64(n)
	}
	centered := make([][]float64, n)
	for i, v := range vectors {
		row := make([]float64, dim)
		for j, x := range v {
			row[j] = float64(x) - mean[j]
		}
		centered[i] = row
	}

	pc1 := powerIteration(centered, nil)
	pc2 := powerIteration(centered, pc1)

	for i, row := range centered {
		out[i][0] = dot(row, pc1)
		out[i][1] = dot(row, pc2)
	}
	return out
}

// powerIteration finds the dominant eigenvector of XᵀX without forming the
// covariance matrix: v ← Xᵀ(Xv), normalized each step. If deflate is
// non-nil, its component is removed each iteration so the second PC emerges.
func powerIteration(x [][]float64, deflate []float64) []float64 {
	dim := len(x[0])
	v := make([]float64, dim)
	// Deterministic non-degenerate start.
	for j := range v {
		v[j] = 1.0 / math.Sqrt(float64(dim))
	}
	if deflate != nil {
		subtractProjection(v, deflate)
	}

	tmp := make([]float64, len(x))
	next := make([]float64, dim)
	for iter := 0; iter < 100; iter++ {
		// tmp = Xv
		for i, row := range x {
			tmp[i] = dot(row, v)
		}
		// next = Xᵀ tmp
		for j := range next {
			next[j] = 0
		}
		for i, row := range x {
			t := tmp[i]
			for j, xj := range row {
				next[j] += xj * t
			}
		}
		if deflate != nil {
			subtractProjection(next, deflate)
		}
		norm := math.Sqrt(dot(next, next))
		if norm < 1e-12 {
			return v // degenerate (e.g. all points identical)
		}
		var delta float64
		for j := range v {
			nj := next[j] / norm
			delta += math.Abs(nj - v[j])
			v[j] = nj
		}
		if delta < 1e-9 {
			break
		}
	}
	return v
}

func subtractProjection(v, onto []float64) {
	p := dot(v, onto)
	for j := range v {
		v[j] -= p * onto[j]
	}
}

func dot(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
