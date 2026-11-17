# Report

Deployment details (URL, demo script, cluster notes) live with the private
deployment repo, not here. This file covers the app itself.

## Milestones

| # | Milestone | Status |
|---|-----------|--------|
| 1 | Single-node renderer, CLI, bare viewer | Shipped. PNG output only; no EXR writer. |
| 2 | Distributed tiles, per-node colours, live samples/s legend | Shipped. Tiles stream over Server-Sent Events rather than WebSocket (see decisions). |
| 3 | Progressive refinement, spp and ETA | Shipped. Passes of 1, 1, 2, 4, then 8 spp. |
| 4 | Fault tolerance, leases, "kill a worker" button | Shipped. Verified by killing a worker mid-render: its leases expired, moved to other nodes, and the image completed. |
| 5 | GGX metal, glass, textures, environment lighting, meshes, scene picker | Shipped, with gaps: meshes load from glTF only (no OBJ loader), and normal maps are ignored. |
| 6 | Camera orbit, gallery, denoiser, Grafana dashboard | Shipped. |

## Renders

In [`gallery/`](gallery/), all rendered by this code:

- `cornell.png` — Cornell box, 768 spp, denoised
- `sponza.png` — Crytek Sponza (Khronos glTF version) under a procedural sun and sky, 192 spp, denoised
- `still-life.png`, `still-life-closeup.png` — glass, gold and copper spheres and Suzanne under a studio HDRI, 1024 spp, not denoised (the denoiser smears reflections and refractions)

## Design decisions

- **Go, standard library only.** One static binary serves as CLI, coordinator
  and worker. Nothing to audit beyond the Go toolchain; the price is roughly
  2–3× slower rays than a tuned C++/Rust tracer with SIMD.
- **Everything is a triangle.** Spheres are tessellated with smooth normals, so
  the BVH and intersection code have a single primitive type. Glass spheres
  show faint faceting at silhouettes.
- **Server-Sent Events instead of WebSocket.** The viewer only listens; SSE is
  plain HTTP, needs no library, passes through reverse proxies and
  forward-auth unchanged, and reconnects by itself. Tiles travel as RGBE
  (4 bytes per pixel, base64) and are tone-mapped in the browser.
- **Workers pull; the coordinator never tracks membership.** A worker is known
  only by the leases it holds. Leases expire after 15 s and the tile is handed
  to whoever asks next. Results are accepted for any tile pass that is still
  open, so a slow-but-alive worker's work is not wasted, and duplicates are
  dropped.
- **Deterministic sampling.** Each pixel sample is seeded from (pixel, sample
  index, job), so a re-leased tile renders identically wherever it lands.
- **Workers send sums of radiance, albedo and normal.** The coordinator
  accumulates them, which gives the denoiser its guide buffers for free.
- **Denoiser.** Edge-avoiding à-trous wavelet filter (5 passes) on
  albedo-demodulated lighting, guided by first-hit albedo and normal. Simple and
  effective on diffuse surfaces; it blurs detail seen *through* glass or in
  mirrors at low sample counts, because the guides describe the first surface only.
- **Firefly clamp.** Indirect contributions are clamped at 40. This is biased
  (caustics lose energy) but removes the worst speckle.
- **Shadow rays treat glass as opaque.** Light through glass arrives only via
  BSDF-sampled paths, so caustics converge slowly.
- **The coordinator keeps the job in memory.** Restarting it loses the render
  in progress; finished renders are on disk. Fine for a demo, wrong for long jobs.
- **Supply chain.** CI runs only on pushes to `main`, never on pull requests.
  Actions and base images are pinned by SHA/digest; scene assets are pinned to
  an upstream commit and verified against `assets/SHA256SUMS` at build time.

## What I'd do differently

- Pack BVH nodes into 32 bytes of float32 and traverse front-to-back by split
  axis; the current float64 nodes are cache-hostile.
- Stratified or low-discrepancy sampling instead of independent random numbers.
- Per-pixel variance from the workers, to drive both adaptive sampling and the
  denoiser's colour weight.
- Record denoiser guides at the first *non-specular* hit so glass and mirrors
  keep their detail.
- Persist the job state so a coordinator restart resumes instead of forgetting.
- The internal lease/result endpoints trust anything that can reach the
  coordinator's port. Behind a private network that is acceptable; a shared
  secret between coordinator and workers would be the next step.

## Assets

Sponza and Suzanne come from the Khronos glTF Sample Assets repository; the
studio HDRI is from Poly Haven (CC0). None are stored in this repository:
`assets/fetch.sh` downloads and checksums them.
