package rt

import "math"

// Denoise is an edge-avoiding à-trous wavelet filter (Dammertz et al. 2010)
// guided by first-hit albedo and normal. buf holds Channels averaged floats
// per pixel; the result is 3 floats per pixel. Lighting is filtered with the
// albedo divided out so texture detail survives.
func Denoise(w, h int, buf []float32) []float32 {
	n := w * h
	cur, next := make([]float32, n*3), make([]float32, n*3)
	for i := 0; i < n; i++ {
		for k := 0; k < 3; k++ {
			cur[3*i+k] = buf[Channels*i+k] / max(buf[Channels*i+3+k], 0.02)
		}
	}
	kernel := [5]float32{1. / 16, 1. / 4, 3. / 8, 1. / 4, 1. / 16}
	// compress HDR values before comparing so highlights don't defeat the colour weight
	tm := func(v float32) float32 { return v / (1 + v) }
	sigC := float32(1.5)
	for it := 0; it < 5; it++ {
		step := 1 << it
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := y*w + x
				g := buf[Channels*i+3 : Channels*i+9]
				var sum [3]float32
				var wsum float32
				for dy := -2; dy <= 2; dy++ {
					yy := y + dy*step
					if yy < 0 || yy >= h {
						continue
					}
					for dx := -2; dx <= 2; dx++ {
						xx := x + dx*step
						if xx < 0 || xx >= w {
							continue
						}
						j := yy*w + xx
						q := buf[Channels*j+3 : Channels*j+9]
						var dc, da, dn float32
						for k := 0; k < 3; k++ {
							d := tm(cur[3*i+k]) - tm(cur[3*j+k])
							dc += d * d
							d = g[k] - q[k]
							da += d * d
							d = g[3+k] - q[3+k]
							dn += d * d
						}
						wt := kernel[dx+2] * kernel[dy+2] *
							float32(math.Exp(float64(-dc/(sigC*sigC)-da/0.02-dn/0.1)))
						for k := 0; k < 3; k++ {
							sum[k] += wt * cur[3*j+k]
						}
						wsum += wt
					}
				}
				for k := 0; k < 3; k++ {
					next[3*i+k] = sum[k] / wsum
				}
			}
		}
		cur, next = next, cur
		sigC /= 2
	}
	for i := 0; i < n; i++ {
		for k := 0; k < 3; k++ {
			cur[3*i+k] *= max(buf[Channels*i+3+k], 0.02)
		}
	}
	return cur
}
