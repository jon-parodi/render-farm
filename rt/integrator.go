package rt

import (
	"math"
	"sort"
)

const (
	maxDepth = 12
	// Indirect contributions are clamped to tame fireflies (caustics seen through
	// diffuse bounces). Slightly biased, hugely less noisy.
	clampIndirect = 40.0
)

// Channels per pixel in a tile buffer: radiance, first-hit albedo, first-hit normal.
const Channels = 9

func power(a, b float64) float64 {
	if a == 0 {
		return 0
	}
	return a * a / (a*a + b*b)
}

// surf is a shading point with texture lookups resolved.
type surf struct {
	base         Vec
	metal, alpha float64
}

// evalBSDF returns the BSDF value and the pdf of sampleBSDF choosing wi.
// wo and wi are in the shading frame (Z = normal).
func (m *surf) eval(wo, wi Vec) (f Vec, pdf float64) {
	if wi.Z <= 0 || wo.Z <= 0 {
		return
	}
	h := wo.Add(wi).Norm()
	voh := wo.Dot(h)
	if voh <= 0 {
		return
	}
	a2 := m.alpha * m.alpha
	den := h.Z*h.Z*(a2-1) + 1
	d := a2 / (math.Pi * den * den)
	g1 := func(c float64) float64 { return 2 * c / (c + math.Sqrt(a2+(1-a2)*c*c)) }
	f0 := Vec{0.04, 0.04, 0.04}.Lerp(m.base, m.metal)
	fr := f0.Add(Vec{1, 1, 1}.Sub(f0).Scale(math.Pow(1-voh, 5)))
	spec := fr.Scale(d * g1(wo.Z) * g1(wi.Z) / (4 * wo.Z * wi.Z))
	diff := m.base.Mul(Vec{1, 1, 1}.Sub(fr)).Scale((1 - m.metal) / math.Pi)
	pS := 0.25 + 0.75*m.metal
	return diff.Add(spec), (1-pS)*wi.Z/math.Pi + pS*d*h.Z/(4*voh)
}

func (m *surf) sample(wo Vec, rng *RNG) Vec {
	u1, u2 := rng.Float(), rng.Float()
	phi := 2 * math.Pi * u2
	if rng.Float() < 0.25+0.75*m.metal {
		a2 := m.alpha * m.alpha
		cos := math.Sqrt((1 - u1) / (1 + (a2-1)*u1))
		sin := math.Sqrt(max(0, 1-cos*cos))
		h := Vec{sin * math.Cos(phi), sin * math.Sin(phi), cos}
		return h.Scale(2 * wo.Dot(h)).Sub(wo)
	}
	r := math.Sqrt(u1)
	return Vec{r * math.Cos(phi), r * math.Sin(phi), math.Sqrt(max(0, 1-u1))}
}

// lookup resolves the material at a hit; hole reports an alpha-masked texel.
func (s *Scene) lookup(tr *Tri, u, v float64) (m *Material, sf surf, hole bool) {
	m = &s.Mats[tr.Mat]
	sf = surf{base: m.Base, metal: m.Metallic}
	rough := m.Rough
	if m.Tex != nil || m.MRTex != nil {
		w := 1 - u - v
		tu := w*float64(tr.UV[0][0]) + u*float64(tr.UV[1][0]) + v*float64(tr.UV[2][0])
		tv := w*float64(tr.UV[0][1]) + u*float64(tr.UV[1][1]) + v*float64(tr.UV[2][1])
		if m.Tex != nil {
			c, a := m.Tex.Sample(tu, tv, false)
			if m.AlphaCut && a < 0.5 {
				return m, sf, true
			}
			sf.base = sf.base.Mul(c)
		}
		if m.MRTex != nil {
			c, _ := m.MRTex.Sample(tu, tv, true)
			rough *= c.Y
			sf.metal *= c.Z
		}
	}
	sf.alpha = max(rough*rough, 0.002)
	return
}

// occluded reports whether anything opaque lies within dist along d.
func (s *Scene) occluded(o, d Vec, dist float64) bool {
	for i := 0; i < 16; i++ {
		t, ti, u, v := s.bvh.Intersect(s.Tris, o, d, dist)
		if ti < 0 {
			return false
		}
		if _, _, hole := s.lookup(&s.Tris[ti], u, v); !hole {
			return true
		}
		o, dist = o.Add(d.Scale(t+s.eps)), dist-t-s.eps
	}
	return true
}

// Trace returns the radiance along a ray plus first-hit albedo and normal
// (guides for the denoiser).
func (s *Scene) Trace(o, d Vec, rng *RNG) (L, alb, nrm Vec) {
	thr := Vec{1, 1, 1}
	bsdfPdf, travelled := 0.0, 0.0
	specular, allSpec, first := true, true, true
	add := func(c Vec) {
		if !allSpec {
			c = c.Min(Vec{clampIndirect, clampIndirect, clampIndirect})
		}
		L = L.Add(c)
	}
	for depth, skips := 0, 0; depth < maxDepth; {
		t, ti, u, v := s.bvh.Intersect(s.Tris, o, d, math.Inf(1))
		if ti < 0 {
			if s.Env != nil {
				w := 1.0
				if !specular {
					w = power(bsdfPdf, s.Env.Pdf(d)*s.pEnv)
				}
				add(thr.Mul(s.Env.Eval(d)).Scale(w))
			}
			if first {
				alb = Vec{1, 1, 1}
			}
			return
		}
		tr := &s.Tris[ti]
		p := o.Add(d.Scale(t))
		m, sf, hole := s.lookup(tr, u, v)
		if hole && skips < 32 {
			o, travelled, skips = p.Add(d.Scale(s.eps)), travelled+t, skips+1
			continue
		}
		t += travelled
		travelled = 0
		ng := tr.E1.Cross(tr.E2).Norm()
		ns := ng
		if tr.N0 != (Vec{}) {
			ns = tr.N0.Scale(1 - u - v).Add(tr.N1.Scale(u)).Add(tr.N2.Scale(v)).Norm()
			if ns.Dot(ng) < 0 {
				ng = ng.Neg() // trust the authored normals over the winding
			}
		}
		wo := d.Neg()
		front := ng.Dot(wo) > 0

		if m.Emit.MaxC() > 0 && front {
			w := 1.0
			if !specular {
				w = power(bsdfPdf, float64(s.lightPdfA[ti])*t*t/ng.Dot(wo)*(1-s.pEnv))
			}
			add(thr.Mul(m.Emit).Scale(w))
		}
		if first {
			first = false
			alb, nrm = sf.base, ns
			if m.Glass {
				alb = Vec{1, 1, 1}
			}
		}

		if m.Glass {
			n, eta := ns, 1/m.IOR
			if !front {
				n, eta = ns.Neg(), m.IOR
			}
			if n.Dot(d) > 0 { // shading normal disagrees at a grazing angle
				n = ng
				if !front {
					n = ng.Neg()
				}
			}
			cosi := -d.Dot(n)
			refl := d.Add(n.Scale(2 * cosi))
			sin2t := eta * eta * (1 - cosi*cosi)
			if sin2t >= 1 {
				d = refl
			} else {
				cost := math.Sqrt(1 - sin2t)
				c := cosi
				if !front {
					c = cost
				}
				r0 := (1 - m.IOR) / (1 + m.IOR)
				r0 *= r0
				if rng.Float() < r0+(1-r0)*math.Pow(1-c, 5) {
					d = refl
				} else {
					d = d.Scale(eta).Add(n.Scale(eta*cosi - cost)).Norm()
					thr = thr.Mul(sf.base)
				}
			}
			o = p.Add(d.Scale(s.eps))
			specular = true
			depth++
			continue
		}

		// opaque surfaces are two-sided
		if !front {
			ng = ng.Neg()
		}
		if ns.Dot(ng) < 0 {
			ns = ns.Neg()
		}
		if ns.Dot(wo) <= 0 {
			ns = ng
		}
		tx, ty := basis(ns)
		woL := Vec{wo.Dot(tx), wo.Dot(ty), wo.Dot(ns)}
		po := p.Add(ng.Scale(s.eps))

		// next-event estimation
		var wi, Li Vec
		lpdf, dist := 0.0, math.Inf(1)
		if rng.Float() < s.pEnv {
			wi, lpdf = s.Env.Sample(rng.Float(), rng.Float())
			lpdf *= s.pEnv
			if lpdf > 0 {
				Li = s.Env.Eval(wi)
			}
		} else if len(s.lights) > 0 {
			k := min(len(s.lights)-1, sort.SearchFloat64s(s.lightCDF, rng.Float()*s.lightCDF[len(s.lightCDF)-1]))
			li := s.lights[k]
			lt := &s.Tris[li]
			a, b := rng.Float(), rng.Float()
			if a+b > 1 {
				a, b = 1-a, 1-b
			}
			q := lt.P0.Add(lt.E1.Scale(a)).Add(lt.E2.Scale(b))
			wi = q.Sub(po)
			dist = wi.Len()
			wi = wi.Scale(1 / dist)
			if cosL := -wi.Dot(lt.E1.Cross(lt.E2).Norm()); cosL > 1e-9 {
				lpdf = float64(s.lightPdfA[li]) * dist * dist / cosL * (1 - s.pEnv)
				Li = s.Mats[lt.Mat].Emit
				dist -= 2 * s.eps
			}
		}
		if lpdf > 0 && wi.Dot(ng) > 0 {
			f, bp := sf.eval(woL, Vec{wi.Dot(tx), wi.Dot(ty), wi.Dot(ns)})
			if bp > 0 && !s.occluded(po, wi, dist) {
				add(thr.Mul(f).Mul(Li).Scale(wi.Dot(ns) / lpdf * power(lpdf, bp)))
			}
		}

		// continue the path by sampling the BSDF
		wiL := sf.sample(woL, rng)
		f, pdf := sf.eval(woL, wiL)
		wi = tx.Scale(wiL.X).Add(ty.Scale(wiL.Y)).Add(ns.Scale(wiL.Z))
		if pdf <= 0 || wi.Dot(ng) <= 0 {
			return
		}
		thr = thr.Mul(f).Scale(wiL.Z / pdf)
		bsdfPdf, specular, allSpec = pdf, false, false
		if depth >= 3 { // Russian roulette
			q := min(thr.MaxC(), 0.95)
			if rng.Float() >= q {
				return
			}
			thr = thr.Scale(1 / q)
		}
		o, d = po, wi
		depth++
	}
	return
}

// RenderTile renders spp samples per pixel for the tile and returns per-pixel
// sums (not averages) of Channels floats. Sample indices start at sampleStart
// so successive passes over the same tile are independent.
func (s *Scene) RenderTile(cam Camera, W, H, x0, y0, w, h, sampleStart, spp int, seed uint64) []float32 {
	out := make([]float32, w*h*Channels)
	fr := cam.frame(float64(W) / float64(H))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			px := out[(y*w+x)*Channels:]
			for i := 0; i < spp; i++ {
				rng := NewRNG(uint64((y0+y)*W+x0+x), uint64(sampleStart+i), seed)
				o, d := fr.ray((float64(x0+x)+rng.Float())/float64(W), (float64(y0+y)+rng.Float())/float64(H), rng)
				L, a, n := s.Trace(o, d, rng)
				if math.IsNaN(L.X+L.Y+L.Z) || math.IsInf(L.X+L.Y+L.Z, 0) {
					L = Vec{}
				}
				for k, c := range [Channels]float64{L.X, L.Y, L.Z, a.X, a.Y, a.Z, n.X, n.Y, n.Z} {
					px[k] += float32(c)
				}
			}
		}
	}
	return out
}
