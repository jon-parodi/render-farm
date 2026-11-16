package rt

import "math"

// Tri is a triangle stored as a vertex plus two edges (Möller–Trumbore form).
type Tri struct {
	P0, E1, E2 Vec
	N0, N1, N2 Vec // shading normals; zero means "use the geometric normal"
	UV         [3][2]float32
	Mat        int32
}

type bvhNode struct {
	min, max Vec
	right    int32 // inner: index of right child (left child is this+1)
	first    int32 // leaf: first triangle
	count    int32 // leaf: triangle count; 0 for inner nodes
}

type BVH struct{ nodes []bvhNode }

func area(mn, mx Vec) float64 {
	d := mx.Sub(mn)
	return d.X*d.Y + d.Y*d.Z + d.Z*d.X
}

var (
	infV    = Vec{math.Inf(1), math.Inf(1), math.Inf(1)}
	negInfV = Vec{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
)

// BuildBVH builds a binned-SAH BVH and reorders tris so leaves are contiguous.
func BuildBVH(tris []Tri) *BVH {
	const bins = 16
	n := len(tris)
	tmin, tmax, cent := make([]Vec, n), make([]Vec, n), make([]Vec, n)
	idx := make([]int32, n)
	for i := range tris {
		t := &tris[i]
		a, b, c := t.P0, t.P0.Add(t.E1), t.P0.Add(t.E2)
		tmin[i], tmax[i] = a.Min(b).Min(c), a.Max(b).Max(c)
		cent[i] = tmin[i].Add(tmax[i]).Scale(0.5)
		idx[i] = int32(i)
	}
	b := &BVH{nodes: make([]bvhNode, 0, 2*n+1)}
	var build func(first, count int32) int32
	build = func(first, count int32) int32 {
		ni := int32(len(b.nodes))
		b.nodes = append(b.nodes, bvhNode{})
		mn, mx, cmn, cmx := infV, negInfV, infV, negInfV
		for _, i := range idx[first : first+count] {
			mn, mx = mn.Min(tmin[i]), mx.Max(tmax[i])
			cmn, cmx = cmn.Min(cent[i]), cmx.Max(cent[i])
		}
		nd := bvhNode{min: mn, max: mx, first: first, count: count}
		if count <= 2 {
			b.nodes[ni] = nd
			return ni
		}
		bestCost, bestAxis, bestSplit := math.Inf(1), -1, 0
		for ax := 0; ax < 3; ax++ {
			lo, ext := cmn.Axis(ax), cmx.Axis(ax)-cmn.Axis(ax)
			if ext <= 0 {
				continue
			}
			var bmn, bmx [bins]Vec
			var bn [bins]int
			for k := range bmn {
				bmn[k], bmx[k] = infV, negInfV
			}
			for _, i := range idx[first : first+count] {
				k := min(bins-1, int(bins*(cent[i].Axis(ax)-lo)/ext))
				bmn[k], bmx[k] = bmn[k].Min(tmin[i]), bmx[k].Max(tmax[i])
				bn[k]++
			}
			// sweep from the right, then evaluate each split from the left
			var rArea [bins]float64
			var rN [bins]int
			rmn, rmx, cnt := infV, negInfV, 0
			for k := bins - 1; k > 0; k-- {
				rmn, rmx = rmn.Min(bmn[k]), rmx.Max(bmx[k])
				cnt += bn[k]
				rArea[k], rN[k] = area(rmn, rmx), cnt
			}
			lmn, lmx, cnt := infV, negInfV, 0
			for k := 0; k < bins-1; k++ {
				lmn, lmx = lmn.Min(bmn[k]), lmx.Max(bmx[k])
				cnt += bn[k]
				if cnt == 0 || rN[k+1] == 0 {
					continue
				}
				if c := area(lmn, lmx)*float64(cnt) + rArea[k+1]*float64(rN[k+1]); c < bestCost {
					bestCost, bestAxis, bestSplit = c, ax, k
				}
			}
		}
		var nl int32
		if bestAxis < 0 || (bestCost >= area(mn, mx)*float64(count) && count <= 8) {
			if count <= 8 {
				b.nodes[ni] = nd
				return ni
			}
			nl = count / 2 // all centroids coincide: split arbitrarily
		} else {
			lo, ext := cmn.Axis(bestAxis), cmx.Axis(bestAxis)-cmn.Axis(bestAxis)
			i, j := first, first+count-1
			for i <= j {
				if min(bins-1, int(bins*(cent[idx[i]].Axis(bestAxis)-lo)/ext)) <= bestSplit {
					i++
				} else {
					idx[i], idx[j] = idx[j], idx[i]
					j--
				}
			}
			nl = i - first
		}
		nd.count = 0
		build(first, nl)
		nd.right = build(first+nl, count-nl)
		b.nodes[ni] = nd
		return ni
	}
	if n > 0 {
		build(0, int32(n))
	}
	sorted := make([]Tri, n)
	for i, j := range idx {
		sorted[i] = tris[j]
	}
	copy(tris, sorted)
	return b
}

func slab(n *bvhNode, o, inv Vec, tmax float64) float64 {
	t1, t2 := (n.min.X-o.X)*inv.X, (n.max.X-o.X)*inv.X
	tn, tf := min(t1, t2), max(t1, t2)
	t1, t2 = (n.min.Y-o.Y)*inv.Y, (n.max.Y-o.Y)*inv.Y
	tn, tf = max(tn, min(t1, t2)), min(tf, max(t1, t2))
	t1, t2 = (n.min.Z-o.Z)*inv.Z, (n.max.Z-o.Z)*inv.Z
	tn, tf = max(tn, min(t1, t2)), min(tf, max(t1, t2))
	if tf >= max(tn, 0) && tn < tmax {
		return tn
	}
	return math.Inf(1)
}

// Intersect returns the closest hit in (1e-9, tmax), or tri = -1.
func (b *BVH) Intersect(tris []Tri, o, d Vec, tmax float64) (t float64, tri int32, u, v float64) {
	t, tri = tmax, -1
	if len(b.nodes) == 0 {
		return
	}
	inv := Vec{1 / d.X, 1 / d.Y, 1 / d.Z}
	var stack [64]int32
	sp := 0
	ni := int32(0)
	if math.IsInf(slab(&b.nodes[0], o, inv, t), 1) {
		return
	}
	for {
		n := &b.nodes[ni]
		if n.count > 0 {
			for i := n.first; i < n.first+n.count; i++ {
				tr := &tris[i]
				p := d.Cross(tr.E2)
				det := tr.E1.Dot(p)
				if det > -1e-14 && det < 1e-14 {
					continue
				}
				id := 1 / det
				tv := o.Sub(tr.P0)
				uu := tv.Dot(p) * id
				if uu < 0 || uu > 1 {
					continue
				}
				q := tv.Cross(tr.E1)
				vv := d.Dot(q) * id
				if vv < 0 || uu+vv > 1 {
					continue
				}
				if tt := tr.E2.Dot(q) * id; tt > 1e-9 && tt < t {
					t, tri, u, v = tt, i, uu, vv
				}
			}
		} else {
			l, r := ni+1, n.right
			tl, tr := slab(&b.nodes[l], o, inv, t), slab(&b.nodes[r], o, inv, t)
			if tl > tr {
				l, r, tl, tr = r, l, tr, tl
			}
			if !math.IsInf(tl, 1) {
				if !math.IsInf(tr, 1) && sp < len(stack) {
					stack[sp] = r
					sp++
				}
				ni = l
				continue
			}
		}
		if sp == 0 {
			return
		}
		sp--
		ni = stack[sp]
	}
}
