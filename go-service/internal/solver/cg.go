package solver

import (
	"fmt"
	"math"
)

// SolveCG solves A*x = b using the preconditioned conjugate gradient method.
// If preconditioner is nil, standard CG is used.
func SolveCG(a *CSRMatrix, b []float64, maxIter int, tol float64, precond Preconditioner) ([]float64, error) {
	n := a.N
	if len(b) != n {
		return nil, fmt.Errorf("dimension mismatch: b length %d != %d", len(b), n)
	}

	x := make([]float64, n)
	r := Copy(b)
	p := make([]float64, n)

	var z []float64
	if precond != nil {
		z = precond.Solve(r)
		copy(p, z)
	} else {
		copy(p, r)
	}

	rz := Dot(r, p)
	if rz == 0 {
		return x, nil
	}

	Ap := make([]float64, n)
	for iter := 0; iter < maxIter; iter++ {
		a.Multiply(p, Ap)
		pAp := Dot(p, Ap)
		if math.Abs(pAp) < 1e-30 {
			fmt.Printf("[PADEN solver] CG breakdown at iter %d: pAp=%g\n", iter, pAp)
			return x, fmt.Errorf("CG breakdown: p^T A p = %g", pAp)
		}
		alpha := rz / pAp
		Axpy(alpha, p, x)
		Axpy(-alpha, Ap, r)

		resNorm := math.Sqrt(Dot(r, r))
		if resNorm < tol {
			fmt.Printf("[PADEN solver] CG converged at iter %d: res=%g\n", iter, resNorm)
			return x, nil
		}

		var rzNew float64
		if precond != nil {
			z = precond.Solve(r)
			rzNew = Dot(r, z)
			beta := rzNew / rz
			for i := 0; i < n; i++ {
				p[i] = z[i] + beta*p[i]
			}
		} else {
			rzNew = Dot(r, r)
			beta := rzNew / rz
			for i := 0; i < n; i++ {
				p[i] = r[i] + beta*p[i]
			}
		}
		if rzNew == 0 {
			return x, nil
		}
		rz = rzNew
	}

	res := math.Sqrt(Dot(r, r))
	fmt.Printf("[PADEN solver] CG did not converge after %d iterations (residual=%g)\n", maxIter, res)
	return x, fmt.Errorf("CG did not converge after %d iterations (residual=%g)", maxIter, res)
}

// Preconditioner is the interface for CG preconditioners.
type Preconditioner interface {
	Solve(r []float64) []float64
}

// JacobiPreconditioner uses the diagonal of A as preconditioner.
type JacobiPreconditioner struct {
	invDiag []float64
}

// NewJacobiPreconditioner creates a Jacobi preconditioner from A's diagonal.
func NewJacobiPreconditioner(a *CSRMatrix) *JacobiPreconditioner {
	d := a.Diagonal()
	inv := make([]float64, len(d))
	for i, v := range d {
		if math.Abs(v) < 1e-12 {
			inv[i] = 1.0
		} else {
			inv[i] = 1.0 / v
		}
	}
	return &JacobiPreconditioner{invDiag: inv}
}

// Solve applies M^{-1} * r.
func (j *JacobiPreconditioner) Solve(r []float64) []float64 {
	z := make([]float64, len(r))
	for i := range r {
		z[i] = j.invDiag[i] * r[i]
	}
	return z
}
