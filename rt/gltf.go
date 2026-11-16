package rt

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
)

// Mat4 is column-major, like glTF.
type Mat4 [16]float64

func Identity() Mat4 { return Mat4{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1} }

func (a Mat4) Mul(b Mat4) (c Mat4) {
	for col := 0; col < 4; col++ {
		for row := 0; row < 4; row++ {
			for k := 0; k < 4; k++ {
				c[col*4+row] += a[k*4+row] * b[col*4+k]
			}
		}
	}
	return
}

func (m Mat4) point(p Vec) Vec {
	return Vec{m[0]*p.X + m[4]*p.Y + m[8]*p.Z + m[12], m[1]*p.X + m[5]*p.Y + m[9]*p.Z + m[13], m[2]*p.X + m[6]*p.Y + m[10]*p.Z + m[14]}
}

// dir transforms a normal; exact for rotation plus uniform scale, which is all we place.
func (m Mat4) dir(p Vec) Vec {
	return Vec{m[0]*p.X + m[4]*p.Y + m[8]*p.Z, m[1]*p.X + m[5]*p.Y + m[9]*p.Z, m[2]*p.X + m[6]*p.Y + m[10]*p.Z}
}

// TRS builds translate × rotateY(deg) × uniform scale.
func TRS(t Vec, rotY, scale float64) Mat4 {
	s, c := math.Sincos(rotY * math.Pi / 180)
	return Mat4{c * scale, 0, -s * scale, 0, 0, scale, 0, 0, s * scale, 0, c * scale, 0, t.X, t.Y, t.Z, 1}
}

type gltfDoc struct {
	Scene  int
	Scenes []struct{ Nodes []int }
	Nodes  []struct {
		Children    []int
		Mesh        *int
		Matrix      []float64
		Translation []float64
		Rotation    []float64
		Scale       []float64
	}
	Meshes []struct {
		Primitives []struct {
			Attributes map[string]int
			Indices    *int
			Material   *int
			Mode       *int
		}
	}
	Accessors []struct {
		BufferView    *int
		ByteOffset    int
		ComponentType int
		Count         int
		Type          string
	}
	BufferViews []struct {
		Buffer, ByteOffset, ByteLength, ByteStride int
	}
	Buffers   []struct{ URI string }
	Images    []struct{ URI string }
	Textures  []struct{ Source *int }
	Materials []struct {
		PBR struct {
			BaseColorFactor          []float64
			BaseColorTexture         *struct{ Index int }
			MetallicFactor           *float64
			RoughnessFactor          *float64
			MetallicRoughnessTexture *struct{ Index int }
		} `json:"pbrMetallicRoughness"`
		EmissiveFactor []float64
		AlphaMode      string
	}
}

// AddGLTF loads a .gltf file (external buffers and images) into the scene,
// transformed by xf. If override >= 0 every primitive uses that material.
func (s *Scene) AddGLTF(path string, xf Mat4, override int32) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var g gltfDoc
	if err := json.Unmarshal(raw, &g); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	dir := filepath.Dir(path)
	// filepath.Base keeps asset URIs inside the model's directory
	bufs := make([][]byte, len(g.Buffers))
	for i, b := range g.Buffers {
		if bufs[i], err = os.ReadFile(filepath.Join(dir, filepath.Base(b.URI))); err != nil {
			return err
		}
	}

	// read returns accessor a as rows of float64 components
	read := func(a int) (rows [][4]float64, err error) {
		if a < 0 || a >= len(g.Accessors) || g.Accessors[a].BufferView == nil {
			return nil, fmt.Errorf("%s: bad accessor %d", path, a)
		}
		ac := g.Accessors[a]
		if *ac.BufferView < 0 || *ac.BufferView >= len(g.BufferViews) {
			return nil, fmt.Errorf("%s: bad buffer view", path)
		}
		bv := g.BufferViews[*ac.BufferView]
		comps := map[string]int{"SCALAR": 1, "VEC2": 2, "VEC3": 3, "VEC4": 4}[ac.Type]
		size := map[int]int{5121: 1, 5123: 2, 5125: 4, 5126: 4}[ac.ComponentType]
		if comps == 0 || size == 0 || bv.Buffer < 0 || bv.Buffer >= len(bufs) {
			return nil, fmt.Errorf("%s: unsupported accessor %d", path, a)
		}
		stride := bv.ByteStride
		if stride == 0 {
			stride = comps * size
		}
		data := bufs[bv.Buffer]
		start := bv.ByteOffset + ac.ByteOffset
		if ac.Count < 0 || start < 0 || (ac.Count > 0 && start+(ac.Count-1)*stride+comps*size > len(data)) {
			return nil, fmt.Errorf("%s: accessor %d out of range", path, a)
		}
		rows = make([][4]float64, ac.Count)
		for i := range rows {
			for c := 0; c < comps; c++ {
				p := data[start+i*stride+c*size:]
				switch ac.ComponentType {
				case 5121:
					rows[i][c] = float64(p[0])
				case 5123:
					rows[i][c] = float64(binary.LittleEndian.Uint16(p))
				case 5125:
					rows[i][c] = float64(binary.LittleEndian.Uint32(p))
				case 5126:
					rows[i][c] = float64(math.Float32frombits(binary.LittleEndian.Uint32(p)))
				}
			}
		}
		return rows, nil
	}

	texCache := map[int]*Texture{}
	tex := func(ref *struct{ Index int }) *Texture {
		if ref == nil || ref.Index < 0 || ref.Index >= len(g.Textures) || g.Textures[ref.Index].Source == nil {
			return nil
		}
		src := *g.Textures[ref.Index].Source
		if t, ok := texCache[src]; ok {
			return t
		}
		var t *Texture
		if src >= 0 && src < len(g.Images) {
			if f, err := os.Open(filepath.Join(dir, filepath.Base(g.Images[src].URI))); err == nil {
				if img, _, err := image.Decode(f); err == nil {
					b := img.Bounds()
					dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
					draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
					t = &Texture{W: b.Dx(), H: b.Dy(), Pix: dst.Pix}
				}
				f.Close()
			}
		}
		texCache[src] = t // a missing texture falls back to the colour factor
		return t
	}

	mats := make([]int32, len(g.Materials))
	for i, gm := range g.Materials {
		m := Material{Base: Vec{1, 1, 1}, Metallic: 1, Rough: 1}
		if f := gm.PBR.BaseColorFactor; len(f) >= 3 {
			m.Base = Vec{f[0], f[1], f[2]}
		}
		if gm.PBR.MetallicFactor != nil {
			m.Metallic = *gm.PBR.MetallicFactor
		}
		if gm.PBR.RoughnessFactor != nil {
			m.Rough = *gm.PBR.RoughnessFactor
		}
		if f := gm.EmissiveFactor; len(f) >= 3 {
			m.Emit = Vec{f[0], f[1], f[2]}
		}
		m.Tex, m.MRTex = tex(gm.PBR.BaseColorTexture), tex(gm.PBR.MetallicRoughnessTexture)
		m.AlphaCut = gm.AlphaMode == "MASK" || gm.AlphaMode == "BLEND"
		mats[i] = s.AddMat(m)
	}
	fallback := int32(-1)

	var walk func(n int, parent Mat4, depth int) error
	walk = func(n int, parent Mat4, depth int) error {
		if n < 0 || n >= len(g.Nodes) || depth > 64 {
			return fmt.Errorf("%s: bad node graph", path)
		}
		nd := g.Nodes[n]
		local := Identity()
		if len(nd.Matrix) == 16 {
			copy(local[:], nd.Matrix)
		} else {
			if q := nd.Rotation; len(q) == 4 {
				x, y, z, w := q[0], q[1], q[2], q[3]
				local = Mat4{1 - 2*(y*y+z*z), 2 * (x*y + z*w), 2 * (x*z - y*w), 0,
					2 * (x*y - z*w), 1 - 2*(x*x+z*z), 2 * (y*z + x*w), 0,
					2 * (x*z + y*w), 2 * (y*z - x*w), 1 - 2*(x*x+y*y), 0, 0, 0, 0, 1}
			}
			if sc := nd.Scale; len(sc) == 3 {
				for c := 0; c < 3; c++ {
					for r := 0; r < 3; r++ {
						local[c*4+r] *= sc[c]
					}
				}
			}
			if t := nd.Translation; len(t) == 3 {
				local[12], local[13], local[14] = t[0], t[1], t[2]
			}
		}
		m := parent.Mul(local)
		if nd.Mesh != nil && *nd.Mesh >= 0 && *nd.Mesh < len(g.Meshes) {
			for _, pr := range g.Meshes[*nd.Mesh].Primitives {
				if pr.Mode != nil && *pr.Mode != 4 {
					continue // triangles only
				}
				pa, ok := pr.Attributes["POSITION"]
				if !ok {
					continue
				}
				pos, err := read(pa)
				if err != nil {
					return err
				}
				var nrm, uv [][4]float64
				if a, ok := pr.Attributes["NORMAL"]; ok {
					if nrm, err = read(a); err != nil || len(nrm) != len(pos) {
						return fmt.Errorf("%s: bad normals", path)
					}
				}
				if a, ok := pr.Attributes["TEXCOORD_0"]; ok {
					if uv, err = read(a); err != nil || len(uv) != len(pos) {
						return fmt.Errorf("%s: bad texcoords", path)
					}
				}
				var idx [][4]float64
				if pr.Indices != nil {
					if idx, err = read(*pr.Indices); err != nil {
						return err
					}
				} else {
					idx = make([][4]float64, len(pos))
					for i := range idx {
						idx[i][0] = float64(i)
					}
				}
				mat := override
				if mat < 0 {
					if pr.Material != nil && *pr.Material >= 0 && *pr.Material < len(mats) {
						mat = mats[*pr.Material]
					} else {
						if fallback < 0 {
							fallback = s.AddMat(Material{Base: Vec{0.8, 0.8, 0.8}, Rough: 1})
						}
						mat = fallback
					}
				}
				for i := 0; i+2 < len(idx); i += 3 {
					var v [3]int
					for k := range v {
						if v[k] = int(idx[i+k][0]); v[k] >= len(pos) {
							return fmt.Errorf("%s: index out of range", path)
						}
					}
					p := func(k int) Vec { return m.point(Vec{pos[v[k]][0], pos[v[k]][1], pos[v[k]][2]}) }
					t := Tri{P0: p(0), E1: p(1).Sub(p(0)), E2: p(2).Sub(p(0)), Mat: mat}
					if t.E1.Cross(t.E2).Len() == 0 {
						continue
					}
					if nrm != nil {
						n := func(k int) Vec { return m.dir(Vec{nrm[v[k]][0], nrm[v[k]][1], nrm[v[k]][2]}).Norm() }
						t.N0, t.N1, t.N2 = n(0), n(1), n(2)
					}
					if uv != nil {
						for k := range v {
							t.UV[k] = [2]float32{float32(uv[v[k]][0]), float32(uv[v[k]][1])}
						}
					}
					s.Tris = append(s.Tris, t)
				}
			}
		}
		for _, c := range nd.Children {
			if err := walk(c, m, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if g.Scene < 0 || g.Scene >= len(g.Scenes) {
		return fmt.Errorf("%s: no scene", path)
	}
	for _, n := range g.Scenes[g.Scene].Nodes {
		if err := walk(n, xf, 0); err != nil {
			return err
		}
	}
	return nil
}
