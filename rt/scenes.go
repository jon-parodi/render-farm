package rt

import (
	"fmt"
	"path/filepath"
)

// SceneInfo describes a built-in scene without loading its geometry, so the
// coordinator can list scenes and default cameras cheaply.
type SceneInfo struct {
	Name     string  `json:"name"`
	Title    string  `json:"title"`
	Cam      Camera  `json:"cam"`
	Exposure float64 `json:"exposure"`
}

var Scenes = []SceneInfo{
	{"cornell", "Cornell box", Camera{Target: Vec{0, 1, 0}, Dist: 4.6, Fov: 35}, 1},
	{"still", "Glass & metal still life", Camera{Target: Vec{0, 0.55, 0}, Yaw: 14, Pitch: 13, Dist: 5.2, Fov: 34, Aperture: 0.025}, 1},
	{"sponza", "Crytek Sponza", Camera{Target: Vec{0.5, 3, -0.2}, Yaw: -90, Pitch: -9, Dist: 9.5, Fov: 66}, 2.5},
}

func Info(name string) (SceneInfo, bool) {
	for _, s := range Scenes {
		if s.Name == name {
			return s, true
		}
	}
	return SceneInfo{}, false
}

// Load builds a scene by name. assets is the directory filled by assets/fetch.sh.
func Load(name, assets string) (*Scene, error) {
	info, ok := Info(name)
	if !ok {
		return nil, fmt.Errorf("unknown scene %q", name)
	}
	s := &Scene{Cam: info.Cam, Exposure: info.Exposure}
	var err error
	switch name {
	case "cornell":
		cornell(s)
	case "still":
		err = still(s, assets)
	case "sponza":
		s.Env = NewSky(Vec{0.35, 1, 0.18}, 900)
		err = s.AddGLTF(filepath.Join(assets, "Sponza", "Sponza.gltf"), Identity(), -1)
	}
	if err != nil {
		return nil, err
	}
	return s.Finish(), nil
}

func cornell(s *Scene) {
	white := s.AddMat(Material{Base: Vec{0.73, 0.73, 0.73}, Rough: 1})
	red := s.AddMat(Material{Base: Vec{0.65, 0.05, 0.05}, Rough: 1})
	green := s.AddMat(Material{Base: Vec{0.12, 0.45, 0.15}, Rough: 1})
	light := s.AddMat(Material{Emit: Vec{18, 15, 9}.Scale(1.5)})
	s.AddQuad(Vec{-1, 0, 1}, Vec{1, 0, 1}, Vec{1, 0, -1}, Vec{-1, 0, -1}, white, 1)   // floor
	s.AddQuad(Vec{-1, 2, -1}, Vec{1, 2, -1}, Vec{1, 2, 1}, Vec{-1, 2, 1}, white, 1)   // ceiling
	s.AddQuad(Vec{-1, 0, -1}, Vec{1, 0, -1}, Vec{1, 2, -1}, Vec{-1, 2, -1}, white, 1) // back
	s.AddQuad(Vec{-1, 0, 1}, Vec{-1, 0, -1}, Vec{-1, 2, -1}, Vec{-1, 2, 1}, red, 1)   // left
	s.AddQuad(Vec{1, 0, -1}, Vec{1, 0, 1}, Vec{1, 2, 1}, Vec{1, 2, -1}, green, 1)     // right
	const l = 0.25
	s.AddQuad(Vec{-l, 1.998, -l}, Vec{l, 1.998, -l}, Vec{l, 1.998, l}, Vec{-l, 1.998, l}, light, 1) // faces down
	s.AddBox(Vec{-0.35, 0, -0.3}, Vec{0.6, 1.2, 0.6}, 18, white)
	s.AddBox(Vec{0.35, 0, 0.3}, Vec{0.6, 0.6, 0.6}, -18, white)
}

func still(s *Scene, assets string) error {
	env, err := LoadHDR(filepath.Join(assets, "studio.hdr"))
	if err != nil {
		return err
	}
	env.Rot = 2.2
	s.Env = env
	floor := s.AddMat(Material{Base: Vec{1, 1, 1}, Tex: Checker([3]uint8{205, 200, 190}, [3]uint8{60, 62, 70}, 8), Rough: 0.3})
	glass := s.AddMat(Material{Base: Vec{1, 1, 1}, Glass: true, IOR: 1.5})
	green := s.AddMat(Material{Base: Vec{0.6, 0.95, 0.7}, Glass: true, IOR: 1.5})
	gold := s.AddMat(Material{Base: Vec{1, 0.77, 0.34}, Metallic: 1, Rough: 0.12})
	copper := s.AddMat(Material{Base: Vec{0.95, 0.64, 0.54}, Metallic: 1, Rough: 0.45})
	const e = 8
	s.AddQuad(Vec{-e, 0, e}, Vec{e, 0, e}, Vec{e, 0, -e}, Vec{-e, 0, -e}, floor, 4)
	s.AddSphere(Vec{-1.25, 0.55, 0.2}, 0.55, glass)
	s.AddSphere(Vec{1.3, 0.55, 0.1}, 0.55, gold)
	s.AddSphere(Vec{0.62, 0.28, 1.05}, 0.28, copper)
	s.AddSphere(Vec{-0.45, 0.22, 1.15}, 0.22, green)
	// Suzanne's lowest vertex is at y = -0.975 in model space
	return s.AddGLTF(filepath.Join(assets, "Suzanne", "Suzanne.gltf"), TRS(Vec{0, 0.66, -0.15}, 0, 0.66), -1)
}
