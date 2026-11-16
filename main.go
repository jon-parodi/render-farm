// renderfarm is a from-scratch distributed path tracer.
//
//	renderfarm render      -scene cornell -o out.png   render locally
//	renderfarm coordinator                              serve the viewer and hand out tiles
//	renderfarm worker      -coordinator http://...      render tiles for a coordinator
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/jon-parodi/render-farm/farm"
	"github.com/jon-parodi/render-farm/rt"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: renderfarm render|coordinator|worker [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	assets := fs.String("assets", env("ASSETS", "assets"), "directory filled by assets/fetch.sh")
	switch os.Args[1] {
	case "render":
		scene := fs.String("scene", "cornell", "scene name")
		out := fs.String("o", "out.png", "output PNG")
		w := fs.Int("w", 768, "width")
		h := fs.Int("h", 512, "height")
		spp := fs.Int("spp", 64, "samples per pixel")
		denoise := fs.Bool("denoise", false, "apply the à-trous denoiser")
		cam := fs.String("cam", "", "camera override: tx,ty,tz,yaw,pitch,dist,fov")
		fs.Parse(os.Args[2:])
		if err := render(*scene, *assets, *out, *cam, *w, *h, *spp, *denoise); err != nil {
			log.Fatal(err)
		}
	case "coordinator":
		addr := fs.String("addr", ":8080", "listen address")
		data := fs.String("data", env("DATA_DIR", "out"), "directory for finished renders")
		local := fs.Int("workers", 0, "also run this many in-process worker threads")
		fs.Parse(os.Args[2:])
		c := farm.NewCoordinator(*data)
		if *local > 0 {
			go farm.RunWorker("http://127.0.0.1"+*addr, *assets, "local", "local", *local)
		}
		log.Fatal(c.ListenAndServe(*addr))
	case "worker":
		url := fs.String("coordinator", env("COORDINATOR_URL", "http://127.0.0.1:8080"), "coordinator base URL")
		threads := fs.Int("threads", runtime.GOMAXPROCS(0), "render threads")
		fs.Parse(os.Args[2:])
		host, _ := os.Hostname()
		farm.RunWorker(*url, *assets, env("NODE_NAME", host), env("POD_NAME", host), *threads)
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}

func render(name, assets, out, cam string, w, h, spp int, denoise bool) error {
	start := time.Now()
	s, err := rt.Load(name, assets)
	if err != nil {
		return err
	}
	if cam != "" {
		c := &s.Cam
		if _, err := fmt.Sscanf(cam, "%g,%g,%g,%g,%g,%g,%g", &c.Target.X, &c.Target.Y, &c.Target.Z, &c.Yaw, &c.Pitch, &c.Dist, &c.Fov); err != nil {
			return fmt.Errorf("-cam: %w", err)
		}
	}
	log.Printf("loaded %s: %d triangles in %v", name, len(s.Tris), time.Since(start).Round(time.Millisecond))
	start = time.Now()
	buf := make([]float32, w*h*rt.Channels)
	rows := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < runtime.GOMAXPROCS(0); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for y := range rows {
				copy(buf[y*w*rt.Channels:], s.RenderTile(s.Cam, w, h, 0, y, w, 1, 0, spp, 1))
			}
		}()
	}
	for y := 0; y < h; y++ {
		rows <- y
	}
	close(rows)
	wg.Wait()
	for i := range buf {
		buf[i] /= float32(spp)
	}
	log.Printf("rendered %dx%d @ %d spp in %v", w, h, spp, time.Since(start).Round(time.Millisecond))
	var rgb []float32
	if denoise {
		rgb = rt.Denoise(w, h, buf)
	} else {
		rgb = make([]float32, w*h*3)
		for i := 0; i < w*h; i++ {
			copy(rgb[3*i:3*i+3], buf[rt.Channels*i:])
		}
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	return rt.WritePNG(f, w, h, rgb, s.Exposure)
}
