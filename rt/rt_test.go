package rt

import (
	"math"
	"testing"
)

// The BVH must agree with a brute-force loop over every triangle.
func TestBVHMatchesBruteForce(t *testing.T) {
	s := &Scene{}
	m := s.AddMat(Material{Base: Vec{1, 1, 1}, Rough: 1})
	rng := NewRNG(1, 2, 3)
	r := func() float64 { return rng.Float()*2 - 1 }
	for i := 0; i < 2000; i++ {
		c := Vec{r(), r(), r()}.Scale(5)
		s.AddTri(c, c.Add(Vec{r(), r(), r()}), c.Add(Vec{r(), r(), r()}), m)
	}
	s.Finish()
	brute := &BVH{nodes: []bvhNode{{min: negInfV, max: infV, count: int32(len(s.Tris))}}}
	for i := 0; i < 5000; i++ {
		o, d := Vec{r(), r(), r()}.Scale(8), Vec{r(), r(), r()}.Norm()
		t1, i1, _, _ := s.bvh.Intersect(s.Tris, o, d, math.Inf(1))
		t2, i2, _, _ := brute.Intersect(s.Tris, o, d, math.Inf(1))
		if i1 != i2 || t1 != t2 {
			t.Fatalf("ray %d: bvh hit %d at %g, brute force hit %d at %g", i, i1, t1, i2, t2)
		}
	}
}

// White furnace: under uniform lighting of radiance 1, a surface can't return
// more light than it receives, and a white diffuse one returns nearly all of
// it. Catches energy bugs in the BSDF, its sampling pdfs and the MIS weights.
func TestFurnace(t *testing.T) {
	pix := make([]float32, 64*32*3)
	for i := range pix {
		pix[i] = 1
	}
	for _, mat := range []Material{
		{Base: Vec{1, 1, 1}, Rough: 1},
		{Base: Vec{1, 1, 1}, Rough: 0.3, Metallic: 1},
		{Base: Vec{1, 1, 1}, Glass: true},
	} {
		s := &Scene{Env: NewEnv(64, 32, pix)}
		s.AddSphere(Vec{}, 1, s.AddMat(mat))
		s.Finish()
		sum, n := 0.0, 20000
		for i := 0; i < n; i++ {
			rng := NewRNG(uint64(i), 7, 9)
			L, _, _ := s.Trace(Vec{rng.Float()*1.2 - 0.6, rng.Float()*1.2 - 0.6, 5}, Vec{0, 0, -1}, rng)
			sum += L.Lum()
		}
		if avg := sum / float64(n); avg < 0.85 || avg > 1.02 {
			t.Errorf("furnace with %+v: mean radiance %.3f, want about 1", mat, avg)
		}
	}
}
