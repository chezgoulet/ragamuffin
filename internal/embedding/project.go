package embedding

import (
	"math"
)

// ProjectionPoint is a single point in a 2D projection.
type ProjectionPoint struct {
	ChunkID    string  `json:"chunk_id"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	SourceFile string  `json:"source_file,omitempty"`
	Label      string  `json:"label,omitempty"`
}

// Projection2D caches the 2D projection result.
type Projection2D struct {
	Points []ProjectionPoint `json:"points"`
}

// ProjectPCA computes a 2D PCA projection of the given vectors (#809).
// Uses power iteration for eigenvalue decomposition — no external deps.
// Input: vectors is [][]float32 where each inner slice has dimension D.
// labels and sourceFiles are optional parallel slices for enrichment.
func ProjectPCA(vectors [][]float32, labels, sourceFiles []string) (*Projection2D, error) {
	if len(vectors) == 0 {
		return &Projection2D{Points: []ProjectionPoint{}}, nil
	}

	n := len(vectors)
	d := len(vectors[0])

	// Convert to float64 for numerical stability
	mat := make([][]float64, n)
	for i := 0; i < n; i++ {
		mat[i] = make([]float64, d)
		for j := 0; j < d; j++ {
			mat[i][j] = float64(vectors[i][j])
		}
	}

	// 1. Compute mean vector
	mean := make([]float64, d)
	for i := 0; i < n; i++ {
		for j := 0; j < d; j++ {
			mean[j] += mat[i][j]
		}
	}
	for j := 0; j < d; j++ {
		mean[j] /= float64(n)
	}

	// 2. Center the data
	centered := make([][]float64, n)
	for i := 0; i < n; i++ {
		centered[i] = make([]float64, d)
		for j := 0; j < d; j++ {
			centered[i][j] = mat[i][j] - mean[j]
		}
	}

	// 3. Build covariance matrix (D×D)
	cov := make([][]float64, d)
	for i := 0; i < d; i++ {
		cov[i] = make([]float64, d)
		for j := 0; j < d; j++ {
			var sum float64
			for k := 0; k < n; k++ {
				sum += centered[k][i] * centered[k][j]
			}
			cov[i][j] = sum / float64(n-1)
		}
	}

	// 4. Power iteration to find top 2 eigenvectors
	eigVecs := make([][]float64, 2)
	for eig := 0; eig < 2; eig++ {
		// Start with random vector
		v := make([]float64, d)
		for j := 0; j < d; j++ {
			v[j] = 1.0 / float64(d) // unit vector along diagonal
		}

		for iter := 0; iter < 100; iter++ {
			// v_new = cov * v
			vNew := make([]float64, d)
			for i := 0; i < d; i++ {
				for j := 0; j < d; j++ {
					vNew[i] += cov[i][j] * v[j]
				}
			}

			// Deflate: subtract projection onto previously found eigenvectors
			for p := 0; p < eig; p++ {
				dot := dotProduct(vNew, eigVecs[p])
				for j := 0; j < d; j++ {
					vNew[j] -= dot * eigVecs[p][j]
				}
			}

			// Normalize
			norm := 0.0
			for j := 0; j < d; j++ {
				norm += vNew[j] * vNew[j]
			}
			norm = math.Sqrt(norm)
			if norm < 1e-15 {
				break
			}
			for j := 0; j < d; j++ {
				vNew[j] /= norm
			}

			// Check convergence
			diff := 0.0
			for j := 0; j < d; j++ {
				dv := vNew[j] - v[j]
				diff += dv * dv
			}
			copy(v, vNew)

			if math.Sqrt(diff) < 1e-10 {
				break
			}
		}

		eigVecs[eig] = v
	}

	// 5. Project each point onto the 2 eigenvectors
	points := make([]ProjectionPoint, n)
	for i := 0; i < n; i++ {
		px := dotProduct(centered[i], eigVecs[0])
		py := dotProduct(centered[i], eigVecs[1])

		var label, sf string
		if i < len(labels) {
			label = labels[i]
		}
		if i < len(sourceFiles) {
			sf = sourceFiles[i]
		}
		if label == "" && i < len(sourceFiles) {
			label = sf
		}

		points[i] = ProjectionPoint{
			X: px, Y: py,
			SourceFile: sf,
			Label:      label,
		}
	}

	return &Projection2D{Points: points}, nil
}

func dotProduct(a, b []float64) float64 {
	var sum float64
	for i := 0; i < len(a) && i < len(b); i++ {
		sum += a[i] * b[i]
	}
	return sum
}
