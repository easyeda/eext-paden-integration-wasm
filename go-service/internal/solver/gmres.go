package solver

import (
	"fmt"
	"math"
)

// SolveGMRES solves A*x = b using restarted GMRES.
// restart is the Krylov subspace dimension before restarting.
// maxIter is the total maximum number of matrix-vector products.
// If precond is non-nil, it is applied to residuals (right preconditioning).
func SolveGMRES(a *CSRMatrix, b []float64, restart, maxIter int, tol float64, precond Preconditioner) ([]float64, error) {
	n := a.N
	if len(b) != n {
		return nil, fmt.Errorf("dimension mismatch: b length %d != %d", len(b), n)
	}

	x := make([]float64, n)
	bnrm := math.Sqrt(Dot(b, b))
	if bnrm == 0 {
		return x, nil
	}

	work := make([]float64, n)
	tolAbs := tol * bnrm

	for outer := 0; outer < maxIter; outer += restart {
		// r = b - A*x
		a.Multiply(x, work)
		for i := range b {
			work[i] = b[i] - work[i]
		}
		if precond != nil {
			work = precond.Solve(work)
		}

		beta := math.Sqrt(Dot(work, work))
		if beta < tolAbs {
			return x, nil
		}

		// Allocate Arnoldi basis
		m := restart
		if outer+restart > maxIter {
			m = maxIter - outer
		}
		V := make([][]float64, m+1)
		for i := range V {
			V[i] = make([]float64, n)
		}
		for i := range work {
			V[0][i] = work[i] / beta
		}

		// Hessenberg matrix H (m+1 x m)
		H := make([][]float64, m+1)
		for i := range H {
			H[i] = make([]float64, m)
		}

		// g = beta*e1, updated by Givens rotations
		g := make([]float64, m+1)
		g[0] = beta

		// Givens cosines/sines
		cs := make([]float64, m)
		sn := make([]float64, m)

		converged := false
		lastJ := -1
		for j := 0; j < m; j++ {
			lastJ = j
			// w = A*V[j]
			a.Multiply(V[j], work)
			if precond != nil {
				work = precond.Solve(work)
			}

			// Arnoldi orthogonalization (modified Gram-Schmidt)
			for i := 0; i <= j; i++ {
				H[i][j] = Dot(work, V[i])
				for k := range work {
					work[k] -= H[i][j] * V[i][k]
				}
			}
			H[j+1][j] = math.Sqrt(Dot(work, work))

			if H[j+1][j] < 1e-30 {
				// Lucky breakdown
				converged = true
				break
			}
			for k := range work {
				V[j+1][k] = work[k] / H[j+1][j]
			}

			// Apply previous Givens rotations to column j of H
			for i := 0; i < j; i++ {
				temp := cs[i]*H[i][j] + sn[i]*H[i+1][j]
				H[i+1][j] = -sn[i]*H[i][j] + cs[i]*H[i+1][j]
				H[i][j] = temp
			}

			// Compute new Givens rotation for H[j][j] and H[j+1][j]
			denom := math.Hypot(H[j][j], H[j+1][j])
			if denom < 1e-30 {
				cs[j] = 1
				sn[j] = 0
			} else {
				cs[j] = H[j][j] / denom
				sn[j] = H[j+1][j] / denom
			}

			// Apply rotation to H and g
			temp := cs[j]*H[j][j] + sn[j]*H[j+1][j]
			H[j+1][j] = -sn[j]*H[j][j] + cs[j]*H[j+1][j]
			H[j][j] = temp

			temp = cs[j]*g[j] + sn[j]*g[j+1]
			g[j+1] = -sn[j]*g[j] + cs[j]*g[j+1]
			g[j] = temp

			res := math.Abs(g[j+1])
			if res < tolAbs {
				converged = true
				break
			}
		}

		// Solve upper triangular system H*y = g for the computed columns.
		// If converged inside the loop, only columns 0..lastJ were built.
		cols := m
		if converged && lastJ >= 0 && lastJ+1 < cols {
			cols = lastJ + 1
		}
		if cols <= 0 {
			cols = 1
		}

		y := make([]float64, cols)
		for i := cols - 1; i >= 0; i-- {
			sum := g[i]
			for k := i + 1; k < cols; k++ {
				sum -= H[i][k] * y[k]
			}
			if math.Abs(H[i][i]) < 1e-30 {
				y[i] = 0
			} else {
				y[i] = sum / H[i][i]
			}
		}

		// x = x + V*y
		for i := 0; i < cols; i++ {
			for k := 0; k < n; k++ {
				x[k] += V[i][k] * y[i]
			}
		}

		if converged {
			return x, nil
		}
	}

	// Compute final residual
	a.Multiply(x, work)
	res := 0.0
	for i := range b {
		d := b[i] - work[i]
		res += d * d
	}
	return x, fmt.Errorf("GMRES did not converge after %d iterations (residual=%g)", maxIter, math.Sqrt(res))
}
