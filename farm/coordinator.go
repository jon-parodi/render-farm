// Package farm is the distributed part: a coordinator that splits a render
// into leased tile tasks and serves the viewer, and workers that render them.
package farm

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jon-parodi/render-farm/rt"
)

//go:embed web
var web embed.FS

const (
	tileSize   = 64
	maxGallery = 60
)

const (
	pending = iota
	leased
	done
)

type task struct {
	pass, tile int
	state      uint8
	expiry     time.Time
	node       string
}

// Lease is what a worker gets: everything needed to render one tile pass.
type Lease struct {
	Job         int64     `json:"job"`
	Task        int       `json:"task"`
	Scene       string    `json:"scene"`
	Cam         rt.Camera `json:"cam"`
	W           int       `json:"w"`
	H           int       `json:"h"`
	X           int       `json:"x"`
	Y           int       `json:"y"`
	TW          int       `json:"tw"`
	TH          int       `json:"th"`
	SampleStart int       `json:"sampleStart"`
	SPP         int       `json:"spp"`
}

type job struct {
	ID       int64     `json:"id"`
	Scene    string    `json:"scene"`
	Cam      rt.Camera `json:"cam"`
	W        int       `json:"w"`
	H        int       `json:"h"`
	SPP      int       `json:"spp"`
	Exposure float64   `json:"exposure"`
	State    string    `json:"state"` // rendering, done, stopped

	tiles     []image.Rectangle
	passSPP   []int
	passStart []int
	tasks     []task
	head      int // every task before head is done
	nDone     int
	buf       []float32 // per-pixel sums, rt.Channels each
	tileSPP   []int
	start     time.Time
	elapsed   time.Duration // frozen when the job ends
	samples   int64
	total     int64
}

type nodeStat struct {
	tiles, samples, recent int64
	sps                    float64
	pods                   map[string]time.Time // last seen
}

type Coordinator struct {
	mu           sync.Mutex
	job          *job
	nodes        map[string]*nodeStat
	subs         map[chan []byte]struct{}
	expired      int64
	kills        int64
	dataDir      string
	leaseTimeout time.Duration
}

func NewCoordinator(dataDir string) *Coordinator {
	c := &Coordinator{nodes: map[string]*nodeStat{}, subs: map[chan []byte]struct{}{}, dataDir: dataDir, leaseTimeout: 15 * time.Second}
	if d, err := time.ParseDuration(os.Getenv("LEASE_TIMEOUT")); err == nil && d > 0 {
		c.leaseTimeout = d
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Printf("gallery disabled: %v", err)
	}
	go c.tick()
	return c
}

func (c *Coordinator) ListenAndServe(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := web.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	mux.HandleFunc("GET /api/scenes", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, rt.Scenes) })
	mux.HandleFunc("POST /api/render", c.handleRender)
	mux.HandleFunc("POST /api/stop", c.handleStop)
	mux.HandleFunc("POST /api/kill", c.handleKill)
	mux.HandleFunc("GET /api/events", c.handleEvents)
	mux.HandleFunc("GET /api/frame", c.handleFrame)
	mux.HandleFunc("GET /api/gallery", c.handleGallery)
	mux.Handle("GET /gallery/", http.StripPrefix("/gallery/", http.FileServer(http.Dir(c.dataDir))))
	mux.HandleFunc("GET /metrics", c.handleMetrics)
	mux.HandleFunc("POST /internal/lease", c.handleLease)
	mux.HandleFunc("POST /internal/result", c.handleResult)
	log.Printf("coordinator listening on %s, lease timeout %v", addr, c.leaseTimeout)
	return http.ListenAndServe(addr, mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// broadcast sends an SSE event to every viewer. Callers hold c.mu.
func (c *Coordinator) broadcast(event string, v any) {
	b, _ := json.Marshal(v)
	msg := []byte("event: " + event + "\ndata: " + string(b) + "\n\n")
	for ch := range c.subs {
		select {
		case ch <- msg:
		default: // too slow: drop it; EventSource reconnects and resyncs from /api/frame
			delete(c.subs, ch)
			close(ch)
		}
	}
}

func finite(vs ...float64) bool {
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

func (c *Coordinator) handleRender(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scene string
		W, H  int
		SPP   int
		Cam   *rt.Camera
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	info, ok := rt.Info(req.Scene)
	if !ok || req.W < 64 || req.W > 1920 || req.H < 64 || req.H > 1080 || req.SPP < 1 || req.SPP > 4096 {
		http.Error(w, "scene, size (64..1920 x 64..1080) or spp (1..4096) out of range", http.StatusBadRequest)
		return
	}
	cam := info.Cam
	if q := req.Cam; q != nil {
		if !finite(q.Target.X, q.Target.Y, q.Target.Z, q.Yaw, q.Pitch, q.Dist, q.Fov) ||
			q.Dist < 0.01 || q.Dist > 1000 || q.Fov < 5 || q.Fov > 120 {
			http.Error(w, "camera out of range", http.StatusBadRequest)
			return
		}
		// only the orbit is client-controlled; lens settings stay with the scene
		cam.Target, cam.Yaw, cam.Dist, cam.Fov = q.Target, q.Yaw, q.Dist, q.Fov
		cam.Pitch = max(-89, min(89, q.Pitch))
	}
	j := &job{ID: time.Now().UnixMilli(), Scene: req.Scene, Cam: cam, W: req.W, H: req.H, SPP: req.SPP,
		Exposure: info.Exposure, State: "rendering", start: time.Now(),
		buf: make([]float32, req.W*req.H*rt.Channels), total: int64(req.W) * int64(req.H) * int64(req.SPP)}
	for y := 0; y < j.H; y += tileSize {
		for x := 0; x < j.W; x += tileSize {
			j.tiles = append(j.tiles, image.Rect(x, y, min(x+tileSize, j.W), min(y+tileSize, j.H)))
		}
	}
	// centre-out, so the interesting part of the frame arrives first
	d := func(t image.Rectangle) int {
		dx, dy := t.Min.X+t.Max.X-j.W, t.Min.Y+t.Max.Y-j.H
		return dx*dx + dy*dy
	}
	sort.SliceStable(j.tiles, func(a, b int) bool { return d(j.tiles[a]) < d(j.tiles[b]) })
	j.tileSPP = make([]int, len(j.tiles))
	// progressive passes: 1, 1, 2, 4, then 8 spp each
	for n, p := 0, 0; n < j.SPP; p++ {
		s := min([]int{1, 1, 2, 4, 8}[min(p, 4)], j.SPP-n)
		j.passSPP, j.passStart = append(j.passSPP, s), append(j.passStart, n)
		n += s
		for t := range j.tiles {
			j.tasks = append(j.tasks, task{pass: p, tile: t})
		}
	}
	c.mu.Lock()
	c.job = j
	c.broadcast("job", j)
	c.mu.Unlock()
	log.Printf("job %d: %s %dx%d @ %d spp, %d tasks", j.ID, j.Scene, j.W, j.H, j.SPP, len(j.tasks))
	writeJSON(w, j)
}

func (c *Coordinator) handleStop(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if j := c.job; j != nil && j.State == "rendering" {
		j.State, j.elapsed = "stopped", time.Since(j.start)
		c.broadcast("job", j)
	}
}

// touch records that a worker pod is alive. Callers hold c.mu.
func (c *Coordinator) touch(node, pod string) *nodeStat {
	n := c.nodes[node]
	if n == nil {
		n = &nodeStat{pods: map[string]time.Time{}}
		c.nodes[node] = n
	}
	n.pods[pod] = time.Now()
	return n
}

func workerID(r *http.Request) (node, pod string, ok bool) {
	node, pod = r.URL.Query().Get("node"), r.URL.Query().Get("pod")
	return node, pod, node != "" && pod != "" && len(node) < 128 && len(pod) < 128
}

func (c *Coordinator) handleLease(w http.ResponseWriter, r *http.Request) {
	node, pod, ok := workerID(r)
	if !ok {
		http.Error(w, "node and pod required", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.touch(node, pod)
	j := c.job
	if j == nil || j.State != "rendering" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	now := time.Now()
	for i := j.head; i < len(j.tasks); i++ {
		t := &j.tasks[i]
		if t.state == done {
			if i == j.head {
				j.head++
			}
			continue
		}
		if t.state == leased {
			if now.Before(t.expiry) {
				continue
			}
			// the worker holding this lease is presumed dead: hand the tile to someone else
			c.expired++
			log.Printf("job %d: lease on task %d expired (was %s), re-leasing to %s", j.ID, i, t.node, node)
			c.broadcast("expired", map[string]any{"node": t.node, "to": node})
		}
		t.state, t.expiry, t.node = leased, now.Add(c.leaseTimeout), node
		tr := j.tiles[t.tile]
		c.broadcast("lease", map[string]any{"x": tr.Min.X, "y": tr.Min.Y, "w": tr.Dx(), "h": tr.Dy(), "node": node})
		writeJSON(w, Lease{Job: j.ID, Task: i, Scene: j.Scene, Cam: j.Cam, W: j.W, H: j.H,
			X: tr.Min.X, Y: tr.Min.Y, TW: tr.Dx(), TH: tr.Dy(), SampleStart: j.passStart[t.pass], SPP: j.passSPP[t.pass]})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *Coordinator) handleResult(w http.ResponseWriter, r *http.Request) {
	node, pod, ok := workerID(r)
	id, _ := strconv.ParseInt(r.URL.Query().Get("job"), 10, 64)
	ti, err := strconv.Atoi(r.URL.Query().Get("task"))
	if !ok || err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, tileSize*tileSize*rt.Channels*4))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ns := c.touch(node, pod)
	j := c.job
	if j == nil || j.ID != id || j.State != "rendering" || ti < 0 || ti >= len(j.tasks) {
		w.WriteHeader(http.StatusGone) // stale job: the worker just moves on
		return
	}
	t := &j.tasks[ti]
	tr := j.tiles[t.tile]
	if len(body) != tr.Dx()*tr.Dy()*rt.Channels*4 {
		http.Error(w, "wrong tile size", http.StatusBadRequest)
		return
	}
	if t.state == done {
		return // a slow worker finished after its tile was re-leased and completed
	}
	for y := 0; y < tr.Dy(); y++ {
		row := j.buf[((tr.Min.Y+y)*j.W+tr.Min.X)*rt.Channels:]
		for i := 0; i < tr.Dx()*rt.Channels; i++ {
			v := math.Float32frombits(binary.LittleEndian.Uint32(body[(y*tr.Dx()*rt.Channels+i)*4:]))
			if v == v && v < 1e12 && v > -1e12 { // drop NaN/Inf rather than poison the sum
				row[i] += v
			}
		}
	}
	spp := j.passSPP[t.pass]
	n := int64(tr.Dx() * tr.Dy() * spp)
	t.state = done
	j.nDone++
	j.tileSPP[t.tile] += spp
	j.samples += n
	ns.tiles++
	ns.samples += n
	ns.recent += n
	c.broadcast("tile", map[string]any{"job": j.ID, "x": tr.Min.X, "y": tr.Min.Y, "w": tr.Dx(), "h": tr.Dy(),
		"node": node, "spp": j.tileSPP[t.tile], "data": base64.StdEncoding.EncodeToString(j.rgbe(tr, nil))})
	if j.nDone == len(j.tasks) {
		j.State, j.elapsed = "done", time.Since(j.start)
		c.broadcast("job", j)
		log.Printf("job %d done in %v", j.ID, j.elapsed.Round(time.Millisecond))
		go c.save(j, j.average())
	}
}

// average returns a copy of the framebuffer divided by each tile's sample count.
func (j *job) average() []float32 {
	out := make([]float32, len(j.buf))
	for t, tr := range j.tiles {
		if j.tileSPP[t] == 0 {
			continue
		}
		inv := 1 / float32(j.tileSPP[t])
		for y := tr.Min.Y; y < tr.Max.Y; y++ {
			a, b := (y*j.W+tr.Min.X)*rt.Channels, (y*j.W+tr.Max.X)*rt.Channels
			for i := a; i < b; i++ {
				out[i] = j.buf[i] * inv
			}
		}
	}
	return out
}

// rgbe encodes a region as RGBE. With rgb == nil it averages the job's own
// buffer (the region must be one tile); otherwise rgb is a W×H×3 image.
func (j *job) rgbe(r image.Rectangle, rgb []float32) []byte {
	out := make([]byte, r.Dx()*r.Dy()*4)
	inv := float32(0)
	if rgb == nil {
		for t, tr := range j.tiles {
			if tr == r && j.tileSPP[t] > 0 {
				inv = 1 / float32(j.tileSPP[t])
			}
		}
	}
	o := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if rgb != nil {
				p := rgb[(y*j.W+x)*3:]
				rt.RGBE(out[o:], p[0], p[1], p[2])
			} else {
				p := j.buf[(y*j.W+x)*rt.Channels:]
				rt.RGBE(out[o:], p[0]*inv, p[1]*inv, p[2]*inv)
			}
			o += 4
		}
	}
	return out
}

// handleFrame returns the whole current image as RGBE, optionally denoised.
func (c *Coordinator) handleFrame(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	j := c.job
	if j == nil {
		c.mu.Unlock()
		http.Error(w, "no job", http.StatusNotFound)
		return
	}
	avg := j.average()
	c.mu.Unlock()
	rgb := make([]float32, j.W*j.H*3)
	if r.URL.Query().Get("denoise") == "1" {
		rgb = rt.Denoise(j.W, j.H, avg)
	} else {
		for i := 0; i < j.W*j.H; i++ {
			copy(rgb[3*i:3*i+3], avg[rt.Channels*i:])
		}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Job", strconv.FormatInt(j.ID, 10))
	w.Write(j.rgbe(image.Rect(0, 0, j.W, j.H), rgb))
}

type galleryItem struct {
	File     string    `json:"file"`
	Denoised string    `json:"denoised"`
	Scene    string    `json:"scene"`
	W        int       `json:"w"`
	H        int       `json:"h"`
	SPP      int       `json:"spp"`
	Seconds  float64   `json:"seconds"`
	Cam      rt.Camera `json:"cam"`
	Time     time.Time `json:"time"`
}

// save writes a finished render (raw and denoised PNG plus metadata) to the gallery.
func (c *Coordinator) save(j *job, avg []float32) {
	base := fmt.Sprintf("%d-%s", j.ID, j.Scene)
	item := galleryItem{base + ".png", base + "-denoised.png", j.Scene, j.W, j.H, j.SPP, j.elapsed.Seconds(), j.Cam, time.Now().UTC()}
	rgb := make([]float32, j.W*j.H*3)
	for i := 0; i < j.W*j.H; i++ {
		copy(rgb[3*i:3*i+3], avg[rt.Channels*i:])
	}
	for name, img := range map[string][]float32{item.File: rgb, item.Denoised: rt.Denoise(j.W, j.H, avg)} {
		var b bytes.Buffer
		err := rt.WritePNG(&b, j.W, j.H, img, j.Exposure)
		if err == nil {
			err = os.WriteFile(filepath.Join(c.dataDir, name), b.Bytes(), 0o644)
		}
		if err != nil {
			log.Printf("gallery: %v", err)
			return
		}
	}
	meta, _ := json.Marshal(item)
	if err := os.WriteFile(filepath.Join(c.dataDir, base+".json"), meta, 0o644); err != nil {
		log.Printf("gallery: %v", err)
		return
	}
	// keep the volume small: drop the oldest renders beyond maxGallery
	items := c.gallery()
	for _, old := range items[min(len(items), maxGallery):] {
		for _, f := range []string{old.File, old.Denoised, strings.TrimSuffix(old.File, ".png") + ".json"} {
			os.Remove(filepath.Join(c.dataDir, filepath.Base(f)))
		}
	}
	c.mu.Lock()
	c.broadcast("gallery", item)
	c.mu.Unlock()
}

// gallery lists saved renders, newest first.
func (c *Coordinator) gallery() []galleryItem {
	items := []galleryItem{}
	files, _ := filepath.Glob(filepath.Join(c.dataDir, "*.json"))
	for _, f := range files {
		var it galleryItem
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &it) == nil {
			items = append(items, it)
		}
	}
	sort.Slice(items, func(a, b int) bool { return items[a].Time.After(items[b].Time) })
	return items
}

func (c *Coordinator) handleGallery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, c.gallery())
}

func (c *Coordinator) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := make(chan []byte, 4096)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	b, _ := json.Marshal(c.job)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.subs, ch)
		c.mu.Unlock()
	}()
	fmt.Fprintf(w, "event: job\ndata: %s\n\n", b)
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(msg); err != nil {
				return
			}
			// drain whatever else is queued before flushing
			for more := true; more; {
				select {
				case m, ok := <-ch:
					if !ok {
						return
					}
					w.Write(m)
				default:
					more = false
				}
			}
			fl.Flush()
		}
	}
}

type nodeView struct {
	Node    string  `json:"node"`
	SPS     float64 `json:"sps"`
	Tiles   int64   `json:"tiles"`
	Workers int     `json:"workers"`
}

type statsView struct {
	Nodes   []nodeView `json:"nodes"`
	Queue   int        `json:"queue"`
	SPP     int        `json:"spp"` // passes every tile has finished
	Target  int        `json:"target"`
	Done    float64    `json:"done"` // 0..1
	ETA     float64    `json:"eta"`  // seconds, -1 unknown
	Elapsed float64    `json:"elapsed"`
	SPS     float64    `json:"sps"`
	State   string     `json:"state"`
}

// stats snapshots progress. Callers hold c.mu.
func (c *Coordinator) stats() statsView {
	s := statsView{Nodes: []nodeView{}, ETA: -1, State: "idle"}
	for name, n := range c.nodes {
		s.Nodes = append(s.Nodes, nodeView{name, n.sps, n.tiles, len(n.pods)})
		s.SPS += n.sps
	}
	sort.Slice(s.Nodes, func(a, b int) bool { return s.Nodes[a].Node < s.Nodes[b].Node })
	if j := c.job; j != nil {
		s.State, s.Target, s.Queue = j.State, j.SPP, len(j.tasks)-j.nDone
		s.SPP = j.SPP
		for _, n := range j.tileSPP {
			s.SPP = min(s.SPP, n)
		}
		s.Done = float64(j.samples) / float64(j.total)
		s.Elapsed = j.elapsed.Seconds()
		if j.State == "rendering" {
			s.Elapsed = time.Since(j.start).Seconds()
			if s.SPS > 0 {
				s.ETA = float64(j.total-j.samples) / s.SPS
			}
		} else {
			s.Queue = 0
		}
	}
	return s
}

// tick updates the per-node samples/s average and pushes stats once a second.
func (c *Coordinator) tick() {
	for range time.Tick(time.Second) {
		c.mu.Lock()
		for name, n := range c.nodes {
			n.sps = 0.6*n.sps + 0.4*float64(n.recent)
			if n.sps < 1 {
				n.sps = 0
			}
			n.recent = 0
			for pod, seen := range n.pods {
				if time.Since(seen) > 10*time.Second {
					delete(n.pods, pod)
				}
			}
			if len(n.pods) == 0 && n.sps == 0 && n.tiles == 0 {
				delete(c.nodes, name)
			}
		}
		c.broadcast("stats", c.stats())
		c.mu.Unlock()
	}
}

func (c *Coordinator) handleMetrics(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats()
	var b strings.Builder
	metric := func(name, typ, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	metric("renderfarm_tiles_done_total", "counter", "Tile passes completed, by node.")
	for name, n := range c.nodes {
		fmt.Fprintf(&b, "renderfarm_tiles_done_total{node=%q} %d\n", name, n.tiles)
	}
	metric("renderfarm_samples_total", "counter", "Camera samples rendered, by node.")
	for name, n := range c.nodes {
		fmt.Fprintf(&b, "renderfarm_samples_total{node=%q} %d\n", name, n.samples)
	}
	metric("renderfarm_samples_per_second", "gauge", "Smoothed sample rate, by node.")
	for name, n := range c.nodes {
		fmt.Fprintf(&b, "renderfarm_samples_per_second{node=%q} %g\n", name, n.sps)
	}
	metric("renderfarm_workers", "gauge", "Worker pods seen in the last 10 s, by node.")
	for name, n := range c.nodes {
		fmt.Fprintf(&b, "renderfarm_workers{node=%q} %d\n", name, len(n.pods))
	}
	metric("renderfarm_queue_depth", "gauge", "Tile passes not yet completed in the current job.")
	fmt.Fprintf(&b, "renderfarm_queue_depth %d\n", s.Queue)
	metric("renderfarm_job_progress", "gauge", "Fraction of the current job's samples rendered.")
	fmt.Fprintf(&b, "renderfarm_job_progress %g\n", s.Done)
	metric("renderfarm_leases_expired_total", "counter", "Tile leases that timed out and were re-queued.")
	fmt.Fprintf(&b, "renderfarm_leases_expired_total %d\n", c.expired)
	metric("renderfarm_worker_kills_total", "counter", "Worker pods deleted with the viewer's kill button.")
	fmt.Fprintf(&b, "renderfarm_worker_kills_total %d\n", c.kills)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	io.WriteString(w, b.String())
}
