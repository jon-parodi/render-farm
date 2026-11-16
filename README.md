# render-farm

A physically based path tracer written from scratch in Go, and a small render
farm around it: one coordinator hands out tiles, any number of stateless
workers render them, and a web viewer watches the image converge live.

No rendering libraries are used. The only dependencies are Go's standard
library (HTTP, JSON, PNG/JPEG decoding).

## Renderer (`rt/`)

- Unidirectional path tracing with next-event estimation and multiple
  importance sampling (power heuristic), Russian roulette after 3 bounces
- Binned-SAH BVH over triangles
- Materials: Lambert + GGX metallic-roughness, smooth glass with refraction,
  base-colour / metallic-roughness textures, alpha cut-outs
- Lights: emissive triangles and importance-sampled environment maps
  (Radiance `.hdr` or a procedural sky with a sun)
- glTF 2.0 mesh loading, thin-lens depth of field
- An edge-avoiding à-trous wavelet denoiser guided by albedo and normals

## Farm (`farm/`)

- Workers lease one 64×64 tile pass at a time over HTTP and post back linear
  float sums (radiance, albedo, normal). Leases expire, so a dead worker's
  tiles go back in the queue.
- Passes are progressive: 1, 1, 2, 4, then 8 samples per pixel each.
- The viewer gets tiles over Server-Sent Events, tone-maps in the browser and
  outlines each tile in the colour of the node that rendered it.
- `/metrics` exposes Prometheus metrics; `grafana/dashboard.json` graphs them.

## Run it

```sh
sh assets/fetch.sh                       # download Sponza, Suzanne and an HDRI (checksummed)
go run . render -scene still -spp 256 -denoise -o still.png
go run . coordinator -workers 4          # viewer on http://localhost:8080
```

To add machines, run `renderfarm worker -coordinator http://host:8080` on each.

Scenes: `cornell`, `still` (glass and metal still life), `sponza`.

See [REPORT.md](REPORT.md) for design decisions and status.
