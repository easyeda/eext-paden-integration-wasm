package solver

import (
	"fmt"
	"math"
	"time"
)

// SolveMINRES solves A*x = b using the MINRES method for symmetric (possibly
// indefinite) systems. It uses short Lanczos recurrences and O(n) memory.
func SolveMINRES(a *CSRMatrix, b []float64, maxIter int, tol float64) ([]float64, error) {
	n := a.N
	if len(b) != n {
		return nil, fmt.Errorf("dimension mismatch: b length %d != %d", len(b), n)
	}

	x := make([]float64, n)

	bnrm := math.Sqrt(Dot(b, b))
	if bnrm == 0 {
		return x, nil
	}
	tolAbs := tol * bnrm

	// Lanczos vectors
	v := make([]float64, n)
	for i := range b {
		v[i] = b[i] / bnrm
	}
	vPrev := make([]float64, n)
	z := make([]float64, n)

	// Givens rotation state. Index k uses rot[k-1] and rot[k-2].
	// rot[0] is the identity rotation for k=1,2 boundary cases.
	c := []float64{1.0, 1.0}
	s := []float64{0.0, 0.0}

	// Search directions w_{k-1} and w_{k-2}
	wPrev := make([]float64, n)
	wPrev2 := make([]float64, n)

	phi := bnrm // residual norm
	beta := 0.0 // beta_k (Lanczos)

	for iter := 0; iter < maxIter; iter++ {
		if iter%25 == 0 {
			fmt.Printf("[PADEN solver] MINRES iter %d, residual=%.6e\n", iter, phi)
			// Yield to the JavaScript event loop so the EasyEDA UI stays responsive.
			time.Sleep(1 * time.Millisecond)
		}

		// z = A * v
		a.Multiply(v, z)

		// alpha = v^T * z
		alpha := Dot(v, z)

		// z = z - alpha * v - beta * vPrev  (beta is beta_k)
		for i := 0; i < n; i++ {
			z[i] -= alpha*v[i] + beta*vPrev[i]
		}

		betaNew := math.Sqrt(Dot(z, z)) // beta_{k+1}

		// QR update of the growing tridiagonal matrix.
		// Need rotations k-2 and k-1. The slice grows lazily.
		idx := len(c) - 1 // index of rotation k-1
		cKm2 := c[idx-1]
		sKm2 := s[idx-1]
		cKm1 := c[idx]
		sKm1 := s[idx]

		// Entries of column k after applying rotations k-2 and k-1.
		delta3 := sKm2 * beta      // R_{k-2,k}
		delta2 := cKm2*cKm1*beta + sKm1*alpha // R_{k-1,k}
		delta1 := -sKm1*cKm2*beta + cKm1*alpha // R_{k,k} before final rotation

		// Final rotation to eliminate betaNew.
		gamma := math.Hypot(delta1, betaNew)
		if gamma < 1e-30 {
			return x, fmt.Errorf("MINRES breakdown: zero pivot")
		}
		ck := delta1 / gamma
		sk := betaNew / gamma

		// Residual update. Keep phi signed; convergence uses its magnitude.
		tau := ck * phi
		phi = -sk * phi

		// Update search direction and solution.
		// w_k = (v - delta2 * w_{k-1} - delta3 * w_{k-2}) / gamma
		w := make([]float64, n)
		for i := 0; i < n; i++ {
			w[i] = (v[i] - delta2*wPrev[i] - delta3*wPrev2[i]) / gamma
		}
		for i := 0; i < n; i++ {
			x[i] += tau * w[i]
		}

		if math.Abs(phi) < tolAbs {
			return x, nil
		}

		// Prepare next Lanczos vector.
		if betaNew < 1e-30 {
			// Lucky breakdown: invariant subspace found.
			return x, nil
		}
		copy(vPrev, v)
		for i := 0; i < n; i++ {
			v[i] = z[i] / betaNew
		}
		beta = betaNew

		// Append new rotation.
		c = append(c, ck)
		s = append(s, sk)

		// Shift search directions.
		wPrev2, wPrev = wPrev, w
	}

	return x, fmt.Errorf("MINRES did not converge after %d iterations (residual=%g)", maxIter, phi)
}
