package solver

import (
	"fmt"
	"math"
)

// Triplet is a single non-zero entry in COO format.
type Triplet struct {
	Row int
	Col int
	Val float64
}

// CSRMatrix is a sparse matrix in Compressed Sparse Row format.
type CSRMatrix struct {
	N   int
	Ap  []int     // row pointers
	Aj  []int     // column indices
	Ax  []float64 // values
	nnz int
}

// NewCSRFromTriplets builds a CSR matrix from unsorted COO triplets.
func NewCSRFromTriplets(n int, triplets []Triplet) *CSRMatrix {
	// Count entries per row
	rowCounts := make([]int, n)
	for _, t := range triplets {
		if t.Row < 0 || t.Row >= n || t.Col < 0 || t.Col >= n {
			continue
		}
		rowCounts[t.Row]++
	}

	ap := make([]int, n+1)
	ap[0] = 0
	for i := 0; i < n; i++ {
		ap[i+1] = ap[i] + rowCounts[i]
	}

	aj := make([]int, len(triplets))
	ax := make([]float64, len(triplets))
	next := make([]int, n)
	copy(next, ap[:n])

	for _, t := range triplets {
		if t.Row < 0 || t.Row >= n || t.Col < 0 || t.Col >= n {
			continue
		}
		idx := next[t.Row]
		aj[idx] = t.Col
		ax[idx] = t.Val
		next[t.Row]++
	}

	// Sort each row by column and combine duplicates
	m := &CSRMatrix{N: n}
	m.Ap = make([]int, n+1)
	m.Ap[0] = 0
	for i := 0; i < n; i++ {
		rowStart := ap[i]
		rowEnd := ap[i+1]
		if rowStart == rowEnd {
			continue
		}

		// Sort by column index
		cols := aj[rowStart:rowEnd]
		vals := ax[rowStart:rowEnd]
		sortRow(cols, vals)

		// Combine duplicates
		prevCol := cols[0]
		accum := vals[0]
		for k := 1; k < len(cols); k++ {
			if cols[k] == prevCol {
				accum += vals[k]
			} else {
				m.Aj = append(m.Aj, prevCol)
				m.Ax = append(m.Ax, accum)
				prevCol = cols[k]
				accum = vals[k]
			}
		}
		m.Aj = append(m.Aj, prevCol)
		m.Ax = append(m.Ax, accum)
		m.Ap[i+1] = len(m.Aj)
	}
	m.nnz = len(m.Ax)
	return m
}

func sortRow(cols []int, vals []float64) {
	n := len(cols)
	if n < 2 {
		return
	}
	// Insertion sort for small rows
	for i := 1; i < n; i++ {
		key := cols[i]
		v := vals[i]
		j := i - 1
		for j >= 0 && cols[j] > key {
			cols[j+1] = cols[j]
			vals[j+1] = vals[j]
			j--
		}
		cols[j+1] = key
		vals[j+1] = v
	}
}

// Multiply computes y = A * x.
func (m *CSRMatrix) Multiply(x, y []float64) {
	if len(x) != m.N || len(y) != m.N {
		panic("dimension mismatch")
	}
	for i := 0; i < m.N; i++ {
		sum := 0.0
		for k := m.Ap[i]; k < m.Ap[i+1]; k++ {
			sum += m.Ax[k] * x[m.Aj[k]]
		}
		y[i] = sum
	}
}

// Diagonal returns the diagonal entries.
func (m *CSRMatrix) Diagonal() []float64 {
	d := make([]float64, m.N)
	for i := 0; i < m.N; i++ {
		for k := m.Ap[i]; k < m.Ap[i+1]; k++ {
			if m.Aj[k] == i {
				d[i] = m.Ax[k]
				break
			}
		}
	}
	return d
}

// NNZ returns the number of non-zero entries.
func (m *CSRMatrix) NNZ() int {
	return m.nnz
}

// ToDense converts the matrix to a dense 2D slice.
func (m *CSRMatrix) ToDense() [][]float64 {
	d := make([][]float64, m.N)
	for i := range d {
		d[i] = make([]float64, m.N)
	}
	for i := 0; i < m.N; i++ {
		for k := m.Ap[i]; k < m.Ap[i+1]; k++ {
			d[i][m.Aj[k]] = m.Ax[k]
		}
	}
	return d
}

// AddDiagonal adds a diagonal matrix to this matrix.
func (m *CSRMatrix) AddDiagonal(diag []float64) *CSRMatrix {
	triplets := make([]Triplet, 0, m.nnz+m.N)
	for i := 0; i < m.N; i++ {
		for k := m.Ap[i]; k < m.Ap[i+1]; k++ {
			triplets = append(triplets, Triplet{Row: i, Col: m.Aj[k], Val: m.Ax[k]})
		}
		if i < len(diag) {
			triplets = append(triplets, Triplet{Row: i, Col: i, Val: diag[i]})
		}
	}
	return NewCSRFromTriplets(m.N, triplets)
}

// ResidualNorm computes ||A*x - b||_2.
func ResidualNorm(a *CSRMatrix, x, b []float64) float64 {
	n := a.N
	y := make([]float64, n)
	a.Multiply(x, y)
	var sum float64
	for i := 0; i < n; i++ {
		d := y[i] - b[i]
		sum += d * d
	}
	return math.Sqrt(sum)
}

// Dot computes the dot product of two vectors.
func Dot(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// Copy returns a copy of a slice.
func Copy(src []float64) []float64 {
	dst := make([]float64, len(src))
	copy(dst, src)
	return dst
}

// Axpy computes y = a*x + y.
func Axpy(a float64, x, y []float64) {
	for i := range x {
		y[i] += a * x[i]
	}
}

// Format implements fmt.Formatter for debugging.
func (m *CSRMatrix) Format(s fmt.State, verb rune) {
	fmt.Fprintf(s, "CSRMatrix(%dx%d, nnz=%d)", m.N, m.N, m.nnz)
}
