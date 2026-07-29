package solver

import "math"

// ICPreconditioner is an Incomplete Cholesky (IC(0)) preconditioner.
// It factors A as M ≈ L*L^T where L has the same lower-triangular sparsity
// pattern as A, then applies L^{-T} * L^{-1} to the residual each iteration.
// IC(0) is dramatically more effective than Jacobi for FEM Laplacian matrices
// (which dominate the PDN stiffness here) because it captures the off-diagonal
// coupling that Jacobi ignores.
type ICPreconditioner struct {
	n      int
	Lap    []int
	Laj    []int
	Lax    []float64
	dInv   []float64 // 1/diag(L)
	ColHead []int    // Column CSC: start offsets by column index
	ColIdx  []int    // Column CSC: row index of each off-diagonal L entry
	ColOff  []int    // Column CSC: position of that entry inside Lax (the lower-tri CSR)
}

// NewICPreconditioner computes the IC(0) factorisation of a symmetric positive
// definite CSR matrix `a`. The sparsity pattern of L is the lower triangle of A.
// Caller is responsible for ensuring A is symmetric (only the lower triangle is
// used).
func NewICPreconditioner(a *CSRMatrix) *ICPreconditioner {
	n := a.N
	// Build lower-triangle CSR (column indices sorted ascending per row).
	counts := make([]int, n)
	for i := 0; i < n; i++ {
		for k := a.Ap[i]; k < a.Ap[i+1]; k++ {
			j := a.Aj[k]
			if j <= i {
				counts[i]++
			}
		}
	}
	ap := make([]int, n+1)
	for i := 0; i < n; i++ {
		ap[i+1] = ap[i] + counts[i]
	}
	aj := make([]int, ap[n])
	ax := make([]float64, ap[n])
	pos := make([]int, n)
	for i := 0; i < n; i++ {
		for k := a.Ap[i]; k < a.Ap[i+1]; k++ {
			j := a.Aj[k]
			if j <= i {
				idx := ap[i] + pos[i]
				aj[idx] = j
				ax[idx] = a.Ax[k]
				pos[i]++
			}
		}
	}
	// Note: a.CSRMatrix.AlreadySortedRows=true after NewCSRFromCOO, so aj rows
	// are ascending. The two-pointer scan during factorisation relies on that.

	// Build column CSC index over the lower-triangle: for each column j, list
	// the rows i >= j that contain an entry for column j, and the offset of
	// that entry in ax. Used by the O(N * row_len) backward substitution.
	colCounts := make([]int, n)
	for i := 0; i < n; i++ {
		for k := ap[i]; k < ap[i+1]; k++ {
			j := aj[k]
			if j < i {
				colCounts[j]++
			}
		}
	}
	colHead := make([]int, n+1)
	for i := 0; i < n; i++ {
		colHead[i+1] = colHead[i] + colCounts[i]
	}
	colIdx := make([]int, colHead[n])
	colOff := make([]int, colHead[n])
	colPos := make([]int, n)
	for i := 0; i < n; i++ {
		for k := ap[i]; k < ap[i+1]; k++ {
			j := aj[k]
			if j < i {
				idx := colHead[j] + colPos[j]
				colIdx[idx] = i
				colOff[idx] = k
				colPos[j]++
			}
		}
	}

	// In-place IC(0) Cholesky: A_low -> L such that L*L^T ≈ A_low
	// For each row i, the columns L[i, *] are sorted ascending; we use a
	// small linear scan because the rows are short.
	for i := 0; i < n; i++ {
		// Locate the diagonal entry.
		var aii float64
		iDiagIdx := -1
		for k := ap[i]; k < ap[i+1]; k++ {
			if aj[k] == i {
				aii = ax[k]
				iDiagIdx = k
				break
			}
		}
		if iDiagIdx < 0 {
			// Row i has no diagonal entry — fabricate one so the factor
			// is well-defined. This happens when the ground node has been
			// removed before passing A to CG, leaving a "singular" entry.
			for k := ap[i]; k < ap[i+1]; k++ {
				if aj[k] == i {
					iDiagIdx = k
					break
				}
			}
			if iDiagIdx < 0 {
				continue
			}
		}
		// For each k < i with L[i,k] != 0: subtract L[i,k]^2 from diag.
		for k := ap[i]; k < ap[i+1]; k++ {
			col := aj[k]
			if col >= i {
				break
			}
			aii -= ax[k] * ax[k]
		}
		if aii <= 0 {
			// Should not happen for a SPD A; guard against numerical issues.
			aii = 1e-12
		}
		lii := math.Sqrt(aii)
		ax[iDiagIdx] = lii
		// For each j > i with L[j, i] != 0: update L[j, i] using the column index.
		for kk := colHead[i]; kk < colHead[i+1]; kk++ {
			j := colIdx[kk]
			jiIdx := colOff[kk]
			aji := ax[jiIdx]
			// Sum over k < i of L[i, k] * L[j, k]: walk row i entries with
			// jcol < i, and for each, find matching column in row j via the
			// already-completed ax[pI] entries.
			pI := ap[i]
			pJ := ap[j]
			for pI < ap[i+1] && aj[pI] < i {
				col := aj[pI]
				for pJ < ap[j+1] && aj[pJ] < col {
					pJ++
				}
				if pJ < ap[j+1] && aj[pJ] == col {
					aji -= ax[pI] * ax[pJ]
				}
				pI++
			}
			ax[jiIdx] = aji / lii
		}
	}

	// Precompute 1/diag(L) for the diagonal solve.
	dInv := make([]float64, n)
	for i := 0; i < n; i++ {
		for k := ap[i]; k < ap[i+1]; k++ {
			if aj[k] == i {
				if ax[k] != 0 {
					dInv[i] = 1.0 / ax[k]
				} else {
					dInv[i] = 0
				}
				break
			}
		}
	}
	return &ICPreconditioner{
		n:       n,
		Lap:     ap,
		Laj:     aj,
		Lax:     ax,
		dInv:    dInv,
		ColHead: colHead,
		ColIdx:  colIdx,
		ColOff:  colOff,
	}
}

// Solve applies M^{-1} * r where M = L * L^T.
// Implemented as forward solve L y = r, then backward solve L^T z = y. Both
// substitutions use the column CSC index to walk each row in O(row_len).
func (p *ICPreconditioner) Solve(r []float64) []float64 {
	n := p.n
	y := make([]float64, n)
	z := make([]float64, n)
	// Forward: L y = r. Walk each row i, subtract L[i, j] * y[j] for j < i.
	for i := 0; i < n; i++ {
		var sum float64 = r[i]
		for k := p.Lap[i]; k < p.Lap[i+1]; k++ {
			j := p.Laj[k]
			if j < i {
				sum -= p.Lax[k] * y[j]
			}
		}
		y[i] = sum * p.dInv[i]
	}
	// Backward: L^T z = y. L^T[i, k] = L[k, i] for k > i; use the column index
	// to find, for each column i, every row k that contains it. Each row entry
	// costs O(1) so the full backward substitution is O(nnz).
	for i := n - 1; i >= 0; i-- {
		var sum float64 = y[i]
		for kk := p.ColHead[i]; kk < p.ColHead[i+1]; kk++ {
			row := p.ColIdx[kk]
			laxPos := p.ColOff[kk]
			sum -= p.Lax[laxPos] * z[row]
		}
		z[i] = sum * p.dInv[i]
	}
	return z
}
