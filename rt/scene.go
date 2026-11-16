package rt

import (
	"math"
	"sort"
)

// Texture is 8-bit sRGB RGBA, sampled bilinearly with repeat wrapping.
type Texture struct {
	W, H int
	Pix  []uint8
}

var srgbLUT = func() (l [256]float64) {
	for i := range l {
		c := float64(i) / 255
		if c <= 0.04045 {
			l[i] = c / 12.92
		} else {
			l[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	return
}()

func wrap(i, n int) int {
	i %= n
	if i < 0 {
		i += n
	}
	return i
}

// Sample returns the colour at (u,v). linear=true skips the sRGB decode
// (data textures such as metallic-roughness).
func (t *Texture) Sample(u, v float64, linear bool) (c Vec, a float64) {
	x, y := u*float64(t.W)-0.5, v*float64(t.H)-0.5
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(x0), y-float64(y0)
	for k := 0; k < 4; k++ {
		w := fx
		if k&1 == 0 {
			w = 1 - fx
		}
		if k&2 == 0 {
			w *= 1 - fy
		} else {
			w *= fy
		}
		p := t.Pix[4*(wrap(y0+k>>1, t.H)*t.W+wrap(x0+k&1, t.W)):]
		if linear {
			c = c.Add(Vec{float64(p[0]), float64(p[1]), float64(p[2])}.Scale(w / 255))
		} else {
			c = c.Add(Vec{srgbLUT[p[0]], srgbLUT[p[1]], srgbLUT[p[2]]}.Scale(w))
		}
		a += w * float64(p[3]) / 255
	}
	return
}

// Material is a small metallic-roughness model: a Lambert lobe plus a GGX
// specular lobe, or a smooth dielectric when Glass is set.
type Material struct {
	Base     Vec
	Tex      *Texture // base colour (sRGB) and alpha
	MRTex    *Texture // glTF metallic-roughness: G = roughness, B = metallic
	Emit     Vec
	Metallic float64
	Rough    float64
	Glass    bool
	IOR      float64
	AlphaCut bool // texture alpha < 0.5 is a hole
}

// Env is an equirectangular environment map with a piecewise-constant
// importance-sampling distribution.
type Env struct {
	W, H   int
	Pix    []float32 // linear RGB
	Rot    float64   // rotation about +Y, radians
	Scale  float64
	pdf    []float32 // per-pixel density over the unit (u,v) square
	rowCDF []float64
	colCDF []float64 // H rows of W+1
}

func NewEnv(w, h int, pix []float32) *Env {
	e := &Env{W: w, H: h, Pix: pix, Scale: 1, pdf: make([]float32, w*h),
		rowCDF: make([]float64, h+1), colCDF: make([]float64, h*(w+1))}
	for y := 0; y < h; y++ {
		sin := math.Sin(math.Pi * (float64(y) + 0.5) / float64(h))
		row := e.colCDF[y*(w+1):]
		for x := 0; x < w; x++ {
			p := pix[3*(y*w+x):]
			// the small floor keeps the pdf non-zero wherever bilinear lookups can be
			wgt := (float64(Vec{float64(p[0]), float64(p[1]), float64(p[2])}.Lum()) + 1e-4) * sin
			e.pdf[y*w+x] = float32(wgt)
			row[x+1] = row[x] + wgt
		}
		e.rowCDF[y+1] = e.rowCDF[y] + row[w]
	}
	total := e.rowCDF[h]
	for i := range e.pdf {
		e.pdf[i] = float32(float64(e.pdf[i]) / total * float64(w*h))
	}
	return e
}

func (e *Env) uv(d Vec) (u, v, sin float64) {
	theta := math.Acos(max(-1, min(1, d.Y)))
	phi := math.Atan2(d.Z, d.X) - e.Rot
	u = phi / (2 * math.Pi)
	u -= math.Floor(u)
	return u, theta / math.Pi, math.Sin(theta)
}

func (e *Env) Eval(d Vec) Vec {
	u, v, _ := e.uv(d)
	x, y := u*float64(e.W)-0.5, v*float64(e.H)-0.5
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(x0), y-float64(y0)
	var c Vec
	for k := 0; k < 4; k++ {
		w := fx
		if k&1 == 0 {
			w = 1 - fx
		}
		if k&2 == 0 {
			w *= 1 - fy
		} else {
			w *= fy
		}
		p := e.Pix[3*(max(0, min(e.H-1, y0+k>>1))*e.W+wrap(x0+k&1, e.W)):]
		c = c.Add(Vec{float64(p[0]), float64(p[1]), float64(p[2])}.Scale(w))
	}
	return c.Scale(e.Scale)
}

// Pdf is the solid-angle density with which Sample would return d.
func (e *Env) Pdf(d Vec) float64 {
	u, v, sin := e.uv(d)
	if sin < 1e-6 {
		return 0
	}
	x, y := min(e.W-1, int(u*float64(e.W))), min(e.H-1, int(v*float64(e.H)))
	return float64(e.pdf[y*e.W+x]) / (2 * math.Pi * math.Pi * sin)
}

func (e *Env) Sample(u1, u2 float64) (d Vec, pdf float64) {
	y := min(e.H-1, sort.SearchFloat64s(e.rowCDF, u1*e.rowCDF[e.H])-1)
	y = max(0, y)
	row := e.colCDF[y*(e.W+1) : (y+1)*(e.W+1)]
	x := max(0, min(e.W-1, sort.SearchFloat64s(row, u2*row[e.W])-1))
	// jitter inside the texel; reusing u1,u2 would correlate, so derive fractions from the CDFs
	fy := (u1*e.rowCDF[e.H] - e.rowCDF[y]) / (e.rowCDF[y+1] - e.rowCDF[y])
	fx := (u2*row[e.W] - row[x]) / (row[x+1] - row[x])
	theta := math.Pi * (float64(y) + fy) / float64(e.H)
	phi := 2*math.Pi*(float64(x)+fx)/float64(e.W) + e.Rot
	sin := math.Sin(theta)
	d = Vec{sin * math.Cos(phi), math.Cos(theta), sin * math.Sin(phi)}
	if sin < 1e-6 {
		return d, 0
	}
	return d, float64(e.pdf[y*e.W+x]) / (2 * math.Pi * math.Pi * sin)
}

// Camera orbits Target; angles are in degrees so the viewer can send them as-is.
type Camera struct {
	Target   Vec     `json:"target"`
	Yaw      float64 `json:"yaw"`
	Pitch    float64 `json:"pitch"`
	Dist     float64 `json:"dist"`
	Fov      float64 `json:"fov"`      // vertical
	Aperture float64 `json:"aperture"` // lens radius; focus is on Target
}

type camFrame struct {
	o, fwd, right, up Vec
	tan, aspect       float64
	lens, focus       float64
}

func (c Camera) frame(aspect float64) camFrame {
	y, p := c.Yaw*math.Pi/180, c.Pitch*math.Pi/180
	off := Vec{math.Cos(p) * math.Sin(y), math.Sin(p), math.Cos(p) * math.Cos(y)}
	o := c.Target.Add(off.Scale(c.Dist))
	fwd := off.Neg()
	right := fwd.Cross(Vec{0, 1, 0}).Norm()
	return camFrame{o, fwd, right, right.Cross(fwd), math.Tan(c.Fov * math.Pi / 360), aspect, c.Aperture, c.Dist}
}

// ray maps screen coordinates in [0,1]² (y down) to a world ray.
func (f *camFrame) ray(sx, sy float64, rng *RNG) (o, d Vec) {
	d = f.fwd.Add(f.right.Scale((2*sx - 1) * f.tan * f.aspect)).Add(f.up.Scale((1 - 2*sy) * f.tan))
	if f.lens == 0 {
		return f.o, d.Norm()
	}
	r, a := f.lens*math.Sqrt(rng.Float()), 2*math.Pi*rng.Float()
	o = f.o.Add(f.right.Scale(r * math.Cos(a))).Add(f.up.Scale(r * math.Sin(a)))
	return o, f.o.Add(d.Scale(f.focus)).Sub(o).Norm()
}

// Scene is immutable after Finish and safe for concurrent rendering.
type Scene struct {
	Tris     []Tri
	Mats     []Material
	Env      *Env
	Cam      Camera
	Exposure float64

	bvh       *BVH
	lights    []int32   // emissive triangles
	lightCDF  []float64 // by area × luminance
	lightPdfA []float32 // per triangle: selection probability / area
	pEnv      float64   // probability NEE samples the environment rather than an area light
	eps       float64
}

func (s *Scene) AddMat(m Material) int32 {
	if m.IOR == 0 {
		m.IOR = 1.5
	}
	s.Mats = append(s.Mats, m)
	return int32(len(s.Mats) - 1)
}

func (s *Scene) AddTri(a, b, c Vec, mat int32) {
	s.Tris = append(s.Tris, Tri{P0: a, E1: b.Sub(a), E2: c.Sub(a), Mat: mat})
}

// AddQuad adds a,b,c,d counter-clockwise as seen from the front; UVs span 0..uvScale.
func (s *Scene) AddQuad(a, b, c, d Vec, mat int32, uvScale float32) {
	s.AddTri(a, b, c, mat)
	s.Tris[len(s.Tris)-1].UV = [3][2]float32{{0, 0}, {uvScale, 0}, {uvScale, uvScale}}
	s.AddTri(a, c, d, mat)
	s.Tris[len(s.Tris)-1].UV = [3][2]float32{{0, 0}, {uvScale, uvScale}, {0, uvScale}}
}

// AddBox adds a box of the given size, rotated rotY degrees about its centre's
// vertical axis, with its base centred at pos.
func (s *Scene) AddBox(pos, size Vec, rotY float64, mat int32) {
	sn, cs := math.Sincos(rotY * math.Pi / 180)
	p := func(x, y, z float64) Vec {
		x, z = (x-0.5)*size.X, (z-0.5)*size.Z
		return pos.Add(Vec{x*cs + z*sn, y * size.Y, -x*sn + z*cs})
	}
	s.AddQuad(p(0, 0, 1), p(1, 0, 1), p(1, 1, 1), p(0, 1, 1), mat, 1) // front
	s.AddQuad(p(1, 0, 0), p(0, 0, 0), p(0, 1, 0), p(1, 1, 0), mat, 1) // back
	s.AddQuad(p(0, 0, 0), p(0, 0, 1), p(0, 1, 1), p(0, 1, 0), mat, 1) // left
	s.AddQuad(p(1, 0, 1), p(1, 0, 0), p(1, 1, 0), p(1, 1, 1), mat, 1) // right
	s.AddQuad(p(0, 1, 1), p(1, 1, 1), p(1, 1, 0), p(0, 1, 0), mat, 1) // top
	s.AddQuad(p(0, 0, 0), p(1, 0, 0), p(1, 0, 1), p(0, 0, 1), mat, 1) // bottom
}

// AddSphere tessellates a UV sphere with smooth normals.
func (s *Scene) AddSphere(c Vec, r float64, mat int32) {
	const nu, nv = 64, 32
	pt := func(i, j int) Vec {
		th, ph := math.Pi*float64(j)/nv, 2*math.Pi*float64(i)/nu
		return Vec{math.Sin(th) * math.Cos(ph), math.Cos(th), math.Sin(th) * math.Sin(ph)}
	}
	tri := func(a, b, d Vec) {
		if a == b || b == d || a == d {
			return // degenerate at the poles
		}
		s.Tris = append(s.Tris, Tri{P0: c.Add(a.Scale(r)), E1: b.Sub(a).Scale(r), E2: d.Sub(a).Scale(r), N0: a, N1: b, N2: d, Mat: mat})
	}
	for j := 0; j < nv; j++ {
		for i := 0; i < nu; i++ {
			a, b, cc, d := pt(i, j), pt(i+1, j), pt(i+1, j+1), pt(i, j+1)
			tri(a, b, cc)
			tri(a, cc, d)
		}
	}
}

// Finish builds the BVH and the light tables. Call once after adding geometry.
func (s *Scene) Finish() *Scene {
	s.bvh = BuildBVH(s.Tris)
	s.lightPdfA = make([]float32, len(s.Tris))
	total := 0.0
	ext := 0.0
	for i := range s.Tris {
		t := &s.Tris[i]
		ext = max(ext, math.Abs(t.P0.X), math.Abs(t.P0.Y), math.Abs(t.P0.Z))
		if e := s.Mats[t.Mat].Emit; e.MaxC() > 0 {
			total += 0.5 * t.E1.Cross(t.E2).Len() * e.Lum()
			s.lights = append(s.lights, int32(i))
			s.lightCDF = append(s.lightCDF, total)
		}
	}
	for k, i := range s.lights {
		prev := 0.0
		if k > 0 {
			prev = s.lightCDF[k-1]
		}
		t := &s.Tris[i]
		s.lightPdfA[i] = float32((s.lightCDF[k] - prev) / total / (0.5 * t.E1.Cross(t.E2).Len()))
	}
	switch {
	case s.Env == nil:
		s.pEnv = 0
	case len(s.lights) == 0:
		s.pEnv = 1
	default:
		s.pEnv = 0.5
	}
	s.eps = 1e-5 * max(1, ext)
	if s.Exposure == 0 {
		s.Exposure = 1
	}
	return s
}
