package main

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path"
	"sort"

	"github.com/gaboose/ldtkbuddy/ldtk"
)

type TilesetShrinker struct {
	LDtk     ldtk.LDtk
	Tilesets map[string]image.Image
}

func NewTilesetShrinker(ldtkPath string) (TilesetShrinker, error) {
	bts, err := os.ReadFile(ldtkPath)
	if err != nil {
		return TilesetShrinker{}, fmt.Errorf("failed to read file: %w", err)
	}

	l, err := ldtk.UnmarshalLDtk(bts)
	if err != nil {
		return TilesetShrinker{}, fmt.Errorf("failed to unmarshal ldtk: %w", err)
	}

	tilesets := map[string]image.Image{}
	for p := range analyzeTilesets(l) {
		ldtkDir := path.Dir(ldtkPath)
		tilesetPath := path.Join(ldtkDir, p)
		if err := func() error {
			f, err := os.Open(tilesetPath)
			if err != nil {
				return fmt.Errorf("failed to load %s: %w", tilesetPath, err)
			}
			defer f.Close()

			img, err := png.Decode(f)
			if err != nil {
				return fmt.Errorf("failed to decode %s: %w", tilesetPath, err)
			}

			tilesets[p] = img
			return nil
		}(); err != nil {
			return TilesetShrinker{}, err
		}
	}

	return TilesetShrinker{
		LDtk:     l,
		Tilesets: tilesets,
	}, nil
}

func (ts *TilesetShrinker) Shrink() error {
	tilesetInfos := analyzeTilesets(ts.LDtk)

	type TilesetChanges struct {
		Width     int64
		Height    int64
		TileIDMap map[int64]int64
	}

	// Build remap
	tilesetChanges := make(map[string]TilesetChanges, len(tilesetInfos))
	for path, ti := range tilesetInfos {
		keys := make([]int64, 0, len(ti.usedTiles))
		for k := range ti.usedTiles {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			return keys[i] < keys[j]
		})

		tileIDMap := make(map[int64]int64, len(keys))
		for i, k := range keys {
			tileIDMap[k] = int64(i)
		}

		sqrt := int64(math.Sqrt(float64(len(tileIDMap)))) + 1
		tilesetChanges[path] = TilesetChanges{
			Width:     sqrt,
			Height:    sqrt,
			TileIDMap: tileIDMap,
		}
	}

	// Apply tileset changes on layer instances
	for _, level := range ts.LDtk.Levels {
		for _, layer := range level.LayerInstances {
			if layer.TilesetRelPath == nil {
				continue
			}

			tc := tilesetChanges[*layer.TilesetRelPath]

			for i := range layer.GridTiles {
				oldT := layer.GridTiles[i].T
				newT := tc.TileIDMap[oldT]
				layer.GridTiles[i].T = newT
				layer.GridTiles[i].Src = []int64{newT % tc.Width * layer.GridSize, newT / tc.Width * layer.GridSize}
			}

			for i := range layer.AutoLayerTiles {
				oldT := layer.AutoLayerTiles[i].T
				newT := tc.TileIDMap[oldT]
				layer.AutoLayerTiles[i].T = newT
				layer.AutoLayerTiles[i].Src = []int64{newT % tc.Width * layer.GridSize, newT / tc.Width * layer.GridSize}
			}
		}
	}

	// Apply tileset changes on tileset defs
	for i, tileset := range ts.LDtk.Defs.Tilesets {
		if tileset.RelPath == nil {
			continue
		}

		tc := tilesetChanges[*tileset.RelPath]

		for _, enumTag := range tileset.EnumTags {
			for j := range enumTag.TileIDS {
				enumTag.TileIDS[j] = tc.TileIDMap[enumTag.TileIDS[j]]
			}
		}

		ts.LDtk.Defs.Tilesets[i].PxWid = tc.Width * tilesetInfos[*tileset.RelPath].gridSize
		ts.LDtk.Defs.Tilesets[i].PxHei = tc.Height * tilesetInfos[*tileset.RelPath].gridSize
	}

	// Apply tileset changes to images
	for p, tc := range tilesetChanges {
		oldImg := ts.Tilesets[p]
		gridSize := tilesetInfos[p].gridSize
		newImg := image.NewRGBA(image.Rect(0, 0, int(tc.Width*gridSize), int(tc.Height*gridSize)))

		fmt.Println(p, tc.TileIDMap)
		for oldT, newT := range tc.TileIDMap {
			newPxX := int((newT % tc.Width) * gridSize)
			newPxY := int(newT / tc.Width * gridSize)
			oldPxX := (int(oldT) % divUp(oldImg.Bounds().Max.X, int(gridSize)) * int(gridSize))
			oldPxY := (int(oldT) / divUp(oldImg.Bounds().Max.X, int(gridSize)) * int(gridSize))
			fmt.Println(oldT, newT, newPxX, newPxY, oldPxX, oldPxY, gridSize, oldImg.Bounds())
			draw.Draw(
				newImg,
				image.Rect(newPxX, newPxY, newPxX+int(gridSize), newPxY+int(gridSize)),
				oldImg,
				image.Pt(oldPxX, oldPxY),
				draw.Src,
			)
		}

		ts.Tilesets[p] = newImg
	}

	return nil
}

type tilesetInfo struct {
	usedTiles map[int64]struct{}
	gridSize  int64
}

func analyzeTilesets(l ldtk.LDtk) map[string]tilesetInfo {
	// Get used tiles over all levels and tilesets
	ret := map[string]tilesetInfo{}
	for _, level := range l.Levels {
		for _, layer := range level.LayerInstances {
			if layer.TilesetRelPath == nil {
				continue
			}

			ti, ok := ret[*layer.TilesetRelPath]
			if !ok {
				ti = tilesetInfo{
					usedTiles: map[int64]struct{}{},
					gridSize:  layer.GridSize,
				}
			}

			for _, tile := range layer.GridTiles {
				ti.usedTiles[tile.T] = struct{}{}
			}

			for _, tile := range layer.AutoLayerTiles {
				ti.usedTiles[tile.T] = struct{}{}
			}

			ret[*layer.TilesetRelPath] = ti
		}
	}

	return ret
}

func divUp(left, right int) int {
	q := left / right
	if left%right != 0 {
		q += 1
	}
	return q
}
