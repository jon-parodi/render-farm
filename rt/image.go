package rt

import (
	"bufio"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"os"
)

// Tonemap applies exposure, the Narkowicz ACES fit and a 2.2 gamma. The viewer
// implements the same curve in JavaScript.
func Tonemap(c, exposure float64) uint8 {
	x := max(0, c*exposure)
	x = x * (2.51*x + 0.03) / (x*(2.43*x+0.59) + 0.14)
	return uint8(math.Pow(min(1, x), 1/2.2)*255 + 0.5)
}

// WritePNG tone-maps linear RGB (3 floats per pixel) to a PNG.
func WritePNG(wr io.Writer, w, h int, rgb []float32, exposure float64) error {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		for k := 0; k < 3; k++ {
			img.Pix[4*i+k] = Tonemap(float64(rgb[3*i+k]), exposure)
		}
		img.Pix[4*i+3] = 255
	}
	return png.Encode(wr, img)
}

// RGBE packs linear RGB into Ward's shared-exponent format, 4 bytes per pixel.
func RGBE(dst []byte, r, g, b float32) {
	m := max(r, g, b)
	if m < 1e-32 {
		dst[0], dst[1], dst[2], dst[3] = 0, 0, 0, 0
		return
	}
	f, e := math.Frexp(float64(m))
	s := float32(f * 256 / float64(m))
	dst[0], dst[1], dst[2], dst[3] = uint8(r*s), uint8(g*s), uint8(b*s), uint8(e+128)
}

// LoadHDR reads a Radiance .hdr (RGBE) file into an environment map.
func LoadHDR(path string) (*Env, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for { // header ends at the first blank line
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("%s: bad header: %w", path, err)
		}
		if line == "\n" {
			break
		}
	}
	var w, h int
	if _, err := fmt.Fscanf(r, "-Y %d +X %d\n", &h, &w); err != nil || w <= 0 || h <= 0 || w > 16384 || h > 8192 {
		return nil, fmt.Errorf("%s: unsupported resolution line", path)
	}
	pix := make([]float32, w*h*3)
	line := make([]byte, w*4)
	for y := 0; y < h; y++ {
		var hd [4]byte
		if _, err := io.ReadFull(r, hd[:]); err != nil {
			return nil, err
		}
		if hd[0] == 2 && hd[1] == 2 && int(hd[2])<<8|int(hd[3]) == w {
			for c := 0; c < 4; c++ { // each channel run-length encoded separately
				for x := 0; x < w; {
					n, err := r.ReadByte()
					if err != nil {
						return nil, err
					}
					if n > 128 {
						v, err := r.ReadByte()
						if err != nil || x+int(n-128) > w {
							return nil, fmt.Errorf("%s: bad run", path)
						}
						for ; n > 128; n-- {
							line[4*x+c] = v
							x++
						}
					} else {
						if n == 0 || x+int(n) > w {
							return nil, fmt.Errorf("%s: bad run", path)
						}
						for ; n > 0; n-- {
							if line[4*x+c], err = r.ReadByte(); err != nil {
								return nil, err
							}
							x++
						}
					}
				}
			}
		} else { // flat scanline
			copy(line, hd[:])
			if _, err := io.ReadFull(r, line[4:]); err != nil {
				return nil, err
			}
		}
		for x := 0; x < w; x++ {
			if e := line[4*x+3]; e != 0 {
				s := float32(math.Ldexp(1, int(e)-136))
				for c := 0; c < 3; c++ {
					pix[3*(y*w+x)+c] = float32(line[4*x+c]) * s
				}
			}
		}
	}
	return NewEnv(w, h, pix), nil
}

// NewSky builds a simple procedural sky with a sun disc, as an environment
// map so it goes through the same importance sampler as an HDRI.
func NewSky(sun Vec, sunRadiance float64) *Env {
	const w, h = 1024, 512
	sun = sun.Norm()
	cosSun := math.Cos(3 * math.Pi / 180)
	pix := make([]float32, w*h*3)
	for y := 0; y < h; y++ {
		th := math.Pi * (float64(y) + 0.5) / h
		for x := 0; x < w; x++ {
			ph := 2 * math.Pi * (float64(x) + 0.5) / w
			d := Vec{math.Sin(th) * math.Cos(ph), math.Cos(th), math.Sin(th) * math.Sin(ph)}
			c := Vec{0.25, 0.22, 0.2} // ground
			if d.Y > 0 {
				c = Vec{1.1, 1.15, 1.2}.Lerp(Vec{0.25, 0.5, 1.1}, math.Pow(d.Y, 0.45))
			}
			if d.Dot(sun) > cosSun {
				c = Vec{1, 0.93, 0.8}.Scale(sunRadiance)
			}
			pix[3*(y*w+x)], pix[3*(y*w+x)+1], pix[3*(y*w+x)+2] = float32(c.X), float32(c.Y), float32(c.Z)
		}
	}
	return NewEnv(w, h, pix)
}

// Checker returns an n×n-square two-colour texture.
func Checker(a, b [3]uint8, n int) *Texture {
	const size = 512
	t := &Texture{W: size, H: size, Pix: make([]uint8, size*size*4)}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			c := a
			if (x*n/size+y*n/size)%2 == 1 {
				c = b
			}
			copy(t.Pix[4*(y*size+x):], []uint8{c[0], c[1], c[2], 255})
		}
	}
	return t
}
