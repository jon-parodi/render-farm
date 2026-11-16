// Package rt is the renderer core: a unidirectional path tracer with
// next-event estimation, MIS, a SAH BVH and Russian roulette.
package rt

import "math"

type Vec struct{ X, Y, Z float64 }

func V(x, y, z float64) Vec             { return Vec{x, y, z} }
func (a Vec) Add(b Vec) Vec             { return Vec{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func (a Vec) Sub(b Vec) Vec             { return Vec{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a Vec) Mul(b Vec) Vec             { return Vec{a.X * b.X, a.Y * b.Y, a.Z * b.Z} }
func (a Vec) Scale(s float64) Vec       { return Vec{a.X * s, a.Y * s, a.Z * s} }
func (a Vec) Dot(b Vec) float64         { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }
func (a Vec) Neg() Vec                  { return Vec{-a.X, -a.Y, -a.Z} }
func (a Vec) Len() float64              { return math.Sqrt(a.Dot(a)) }
func (a Vec) Lum() float64              { return 0.2126*a.X + 0.7152*a.Y + 0.0722*a.Z }
func (a Vec) MaxC() float64             { return math.Max(a.X, math.Max(a.Y, a.Z)) }
func (a Vec) Min(b Vec) Vec             { return Vec{math.Min(a.X, b.X), math.Min(a.Y, b.Y), math.Min(a.Z, b.Z)} }
func (a Vec) Max(b Vec) Vec             { return Vec{math.Max(a.X, b.X), math.Max(a.Y, b.Y), math.Max(a.Z, b.Z)} }
func (a Vec) Axis(i int) float64        { return [3]float64{a.X, a.Y, a.Z}[i] }
func (a Vec) Lerp(b Vec, t float64) Vec { return a.Scale(1 - t).Add(b.Scale(t)) }

func (a Vec) Cross(b Vec) Vec {
	return Vec{a.Y*b.Z - a.Z*b.Y, a.Z*b.X - a.X*b.Z, a.X*b.Y - a.Y*b.X}
}

func (a Vec) Norm() Vec {
	l := a.Len()
	if l == 0 {
		return a
	}
	return a.Scale(1 / l)
}

// basis returns two unit vectors orthogonal to the unit vector n.
func basis(n Vec) (t, b Vec) {
	if math.Abs(n.X) > 0.9 {
		t = Vec{0, 1, 0}
	} else {
		t = Vec{1, 0, 0}
	}
	t = t.Sub(n.Scale(t.Dot(n))).Norm()
	return t, n.Cross(t)
}

// RNG is a small xorshift64* generator; one per pixel sample, seeded from a
// hash so any tile renders identically on any worker.
type RNG struct{ s uint64 }

func NewRNG(a, b, c uint64) *RNG {
	s := a*0x9E3779B97F4A7C15 ^ b*0xC2B2AE3D27D4EB4F ^ c*0x165667B19E3779F9
	s ^= s >> 31
	s *= 0xBF58476D1CE4E5B9
	s ^= s >> 29
	return &RNG{s | 1}
}

func (r *RNG) Float() float64 {
	r.s ^= r.s >> 12
	r.s ^= r.s << 25
	r.s ^= r.s >> 27
	return float64((r.s*0x2545F4914F6CDD1D)>>11) / (1 << 53)
}
