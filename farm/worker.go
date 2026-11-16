package farm

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/jon-parodi/render-farm/rt"
)

// sceneCache keeps the one scene the current job needs.
type sceneCache struct {
	mu     sync.Mutex
	name   string
	scene  *rt.Scene
	assets string
}

func (c *sceneCache) get(name string) (*rt.Scene, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.name == name && c.scene != nil {
		return c.scene, nil
	}
	c.scene = nil // let the old scene's textures go before loading the next
	start := time.Now()
	s, err := rt.Load(name, c.assets)
	if err != nil {
		return nil, err
	}
	log.Printf("loaded %s: %d triangles in %v", name, len(s.Tris), time.Since(start).Round(time.Millisecond))
	c.name, c.scene = name, s
	return s, nil
}

// RunWorker leases tiles from the coordinator and renders them, forever.
// Workers hold no state the coordinator needs: killing one loses at most the
// tiles it had leased, which time out and are handed to another worker.
func RunWorker(coordinator, assets, node, pod string, threads int) {
	log.Printf("worker %s on %s: %d threads, coordinator %s", pod, node, threads, coordinator)
	cache := &sceneCache{assets: assets}
	client := &http.Client{Timeout: 30 * time.Second}
	q := "?" + url.Values{"node": {node}, "pod": {pod}}.Encode()
	var wg sync.WaitGroup
	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if err := workOnce(client, coordinator, q, cache); err != nil {
					if err != errIdle {
						log.Print(err)
						time.Sleep(2 * time.Second)
					}
					time.Sleep(400 * time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()
}

var errIdle = fmt.Errorf("no work")

func workOnce(client *http.Client, coordinator, q string, cache *sceneCache) error {
	resp, err := client.Post(coordinator+"/internal/lease"+q, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return errIdle
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("lease: %s", resp.Status)
	}
	var l Lease
	if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
		return fmt.Errorf("lease: %w", err)
	}
	s, err := cache.get(l.Scene)
	if err != nil {
		return err
	}
	// the job id seeds the sampler, so re-rendering a re-leased tile gives the same pixels
	px := s.RenderTile(l.Cam, l.W, l.H, l.X, l.Y, l.TW, l.TH, l.SampleStart, l.SPP, uint64(l.Job))
	body := make([]byte, len(px)*4)
	for i, v := range px {
		binary.LittleEndian.PutUint32(body[i*4:], math.Float32bits(v))
	}
	res, err := client.Post(fmt.Sprintf("%s/internal/result%s&job=%d&task=%d", coordinator, q, l.Job, l.Task),
		"application/octet-stream", bytes.NewReader(body))
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}
