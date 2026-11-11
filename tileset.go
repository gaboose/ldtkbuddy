package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path"
	"sort"
	"strings"

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
	tilesetInfos, err := analyzeTilesets(l)
	if err != nil {
		return TilesetShrinker{}, fmt.Errorf("failed to analyze tilesets: %w", err)
	}
	for p := range tilesetInfos {
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
	tilesetInfos, err := analyzeTilesets(ts.LDtk)
	if err != nil {
		return fmt.Errorf("failed to analyze assets: %w", err)
	}

	type TilesetChanges struct {
		Width     int64
		Height    int64
		TileIDMap map[int64]int64
		// Free-form rects (entity field tiles) copied below the tile grid.
		RectPos map[rectKey]rectKey
	}

	// Tile rects referenced by entity fields, grouped by tileset path.
	uidToPath := map[int64]string{}
	for _, def := range ts.LDtk.Defs.Tilesets {
		if def.RelPath != nil {
			uidToPath[def.Uid] = *def.RelPath
		}
	}
	rectRefs := collectRectRefs(ts.LDtk)
	rectsByPath := map[string]map[rectKey]struct{}{}
	for _, r := range rectRefs {
		p, ok := uidToPath[r.key().uid]
		if _, used := tilesetInfos[p]; !ok || !used {
			continue
		}
		if rectsByPath[p] == nil {
			rectsByPath[p] = map[rectKey]struct{}{}
		}
		rectsByPath[p][r.key()] = struct{}{}
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

		n := int64(len(tileIDMap))
		w := int64(math.Ceil(math.Sqrt(float64(n))))
		if w == 0 {
			w = 1
		}

		// Pack field rects in shelves below the tile grid, keeping them
		// intact. The atlas is widened if a rect doesn't fit.
		grid := ti.gridSize
		rects := make([]rectKey, 0, len(rectsByPath[path]))
		for k := range rectsByPath[path] {
			rects = append(rects, k)
		}
		sort.Slice(rects, func(i, j int) bool {
			a, b := rects[i], rects[j]
			if a.h != b.h {
				return a.h > b.h
			}
			if a.w != b.w {
				return a.w > b.w
			}
			if a.x != b.x {
				return a.x < b.x
			}
			return a.y < b.y
		})
		for _, r := range rects {
			if cells := divUp(int(r.w), int(grid)); int64(cells) > w {
				w = int64(cells)
			}
		}
		rows := int64(divUp(int(n), int(w)))
		if n == 0 {
			rows = 0
		}
		rectPos := make(map[rectKey]rectKey, len(rects))
		var curX, shelfY, shelfH int64
		shelfY = rows * grid
		for _, r := range rects {
			if curX+r.w > w*grid {
				shelfY += shelfH
				curX, shelfH = 0, 0
			}
			rectPos[r] = rectKey{uid: r.uid, x: curX, y: shelfY, w: r.w, h: r.h}
			curX += r.w
			shelfH = max(shelfH, r.h)
		}
		height := int64(divUp(int(shelfY+shelfH), int(grid)))
		if height < 1 {
			height = 1
		}
		if n > 0 && rows > height {
			height = rows
		}

		tilesetChanges[path] = TilesetChanges{
			Width:     w,
			Height:    height,
			TileIDMap: tileIDMap,
			RectPos:   rectPos,
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

	// Point entity field tiles at their new position in the atlas.
	for _, r := range rectRefs {
		k := r.key()
		p, ok := uidToPath[k.uid]
		if !ok {
			continue
		}
		if np, ok := tilesetChanges[p].RectPos[k]; ok {
			r.set(np.x, np.y)
		}
	}

	// Drop tileset defs no layer uses.
	kept := ts.LDtk.Defs.Tilesets[:0]
	for _, tileset := range ts.LDtk.Defs.Tilesets {
		if tileset.RelPath == nil {
			continue
		}
		if _, ok := tilesetChanges[*tileset.RelPath]; ok {
			kept = append(kept, tileset)
		}
	}
	ts.LDtk.Defs.Tilesets = kept

	// Apply tileset changes on tileset defs
	for i, tileset := range ts.LDtk.Defs.Tilesets {
		if tileset.RelPath == nil {
			continue
		}

		tc, ok := tilesetChanges[*tileset.RelPath]
		if !ok {
			continue
		}

		// Drop tags on tiles that were trimmed away. Indexing the map directly
		// would turn every such id into 0 and tag the first kept tile instead.
		for k, enumTag := range tileset.EnumTags {
			kept := enumTag.TileIDS[:0]
			for _, id := range enumTag.TileIDS {
				if newID, ok := tc.TileIDMap[id]; ok {
					kept = append(kept, newID)
				}
			}
			ts.LDtk.Defs.Tilesets[i].EnumTags[k].TileIDS = kept
		}

		// Remap custom data: tile IDs and relative animation offsets.
		// Offsets must be resolved against the OLD column count before CWid is updated.
		oldCWid := tileset.CWid
		newCustomData := tileset.CustomData[:0:0]
		for _, cd := range tileset.CustomData {
			newTileID, ok := tc.TileIDMap[cd.TileID]
			if !ok {
				continue
			}

			offsets, err := parseAnimationOffsets(cd.Data)
			if err != nil {
				return fmt.Errorf("parsing animationOffsets of tile %d in %s: %w", cd.TileID, *tileset.RelPath, err)
			}

			if len(offsets) > 0 {
				newOffsets := make([]Offset, 0, len(offsets))
				for _, o := range offsets {
					oldRef := cd.TileID + o.X + o.Y*oldCWid
					newRef, ok := tc.TileIDMap[oldRef]
					if !ok {
						return fmt.Errorf("tile %d in %s: animation frame tile %d missing from remap", cd.TileID, *tileset.RelPath, oldRef)
					}
					newOffsets = append(newOffsets, Offset{
						X: newRef%tc.Width - newTileID%tc.Width,
						Y: newRef/tc.Width - newTileID/tc.Width,
					})
				}
				cd.Data = replaceAnimationOffsets(cd.Data, newOffsets)
			}

			cd.TileID = newTileID
			newCustomData = append(newCustomData, cd)
		}
		ts.LDtk.Defs.Tilesets[i].CustomData = newCustomData

		ts.LDtk.Defs.Tilesets[i].CWid = tc.Width
		ts.LDtk.Defs.Tilesets[i].CHei = tc.Height
		ts.LDtk.Defs.Tilesets[i].PxWid = tc.Width * tilesetInfos[*tileset.RelPath].gridSize
		ts.LDtk.Defs.Tilesets[i].PxHei = tc.Height * tilesetInfos[*tileset.RelPath].gridSize
	}

	// Apply tileset changes to images
	for p, tc := range tilesetChanges {
		oldImg := ts.Tilesets[p]
		gridSize := tilesetInfos[p].gridSize
		newImg := image.NewRGBA(image.Rect(0, 0, int(tc.Width*gridSize), int(tc.Height*gridSize)))

		for oldT, newT := range tc.TileIDMap {
			newPxX := int((newT % tc.Width) * gridSize)
			newPxY := int(newT / tc.Width * gridSize)
			oldPxX := (int(oldT) % divUp(oldImg.Bounds().Max.X, int(gridSize)) * int(gridSize))
			oldPxY := (int(oldT) / divUp(oldImg.Bounds().Max.X, int(gridSize)) * int(gridSize))
			draw.Draw(
				newImg,
				image.Rect(newPxX, newPxY, newPxX+int(gridSize), newPxY+int(gridSize)),
				oldImg,
				image.Pt(oldPxX, oldPxY),
				draw.Src,
			)
		}

		for oldR, newR := range tc.RectPos {
			draw.Draw(
				newImg,
				image.Rect(int(newR.x), int(newR.y), int(newR.x+newR.w), int(newR.y+newR.h)),
				oldImg,
				image.Pt(int(oldR.x), int(oldR.y)),
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

func analyzeTilesets(l ldtk.LDtk) (map[string]tilesetInfo, error) {
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

	// Get tiles referenced in tileset custom data
	for _, ts := range l.Defs.Tilesets {
		if ts.RelPath == nil {
			continue
		}
		ti, ok := ret[*ts.RelPath]
		if !ok {
			continue
		}

		for _, cd := range ts.CustomData {
			if _, ok := ti.usedTiles[cd.TileID]; !ok {
				continue
			}

			offsets, err := parseAnimationOffsets(cd.Data)
			if err != nil {
				return nil, fmt.Errorf("parsing animationOffset: %w", err)
			}

			for _, o := range offsets {
				refID := cd.TileID + o.X + o.Y*ts.CWid
				ti.usedTiles[refID] = struct{}{}
			}
		}

		ret[*ts.RelPath] = ti
	}

	return ret, nil
}

// Offset is a single animation frame offset.
type Offset struct {
	X, Y int64
}

const animationKey = "animationOffsets"

func parseAnimationOffsets(data string) ([]Offset, error) {
	// Custom data is free text and may hold several keys, one per line.
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)

		rest, ok := strings.CutPrefix(line, animationKey+" ")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return nil, fmt.Errorf("%s has no values", animationKey)
		}

		var pairs [][]int64
		if err := json.Unmarshal([]byte("["+rest+"]"), &pairs); err != nil {
			return nil, fmt.Errorf("parsing %q: %w", rest, err)
		}

		offsets := make([]Offset, 0, len(pairs))
		for i, p := range pairs {
			if len(p) != 2 {
				return nil, fmt.Errorf("offset %d has %d values, want 2", i, len(p))
			}
			offsets = append(offsets, Offset{X: p[0], Y: p[1]})
		}
		return offsets, nil
	}
	return nil, nil
}

// replaceAnimationOffsets rewrites the animationOffsets line in custom data
// leaving any other lines untouched.
func replaceAnimationOffsets(data string, offsets []Offset) string {
	lines := strings.Split(data, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), animationKey+" ") {
			continue
		}
		parts := make([]string, len(offsets))
		for j, o := range offsets {
			parts[j] = fmt.Sprintf("[%d,%d]", o.X, o.Y)
		}
		lines[i] = animationKey + " " + strings.Join(parts, ",")
		break
	}
	return strings.Join(lines, "\n")
}

func divUp(left, right int) int {
	q := left / right
	if left%right != 0 {
		q += 1
	}
	return q
}

// rectKey is a pixel rect in a tileset image.
type rectKey struct{ uid, x, y, w, h int64 }

// rectRef is one tile rect stored on an entity: its __tile, a Tile field or
// an element of an Array<Tile> field.
type rectRef struct {
	k   func() rectKey
	put func(x, y int64)
}

func (r rectRef) key() rectKey   { return r.k() }
func (r rectRef) set(x, y int64) { r.put(x, y) }

func collectRectRefs(l ldtk.LDtk) []rectRef {
	var out []rectRef
	fromRect := func(t *ldtk.TilesetRectangle) {
		if t == nil {
			return
		}
		out = append(out, rectRef{
			k:   func() rectKey { return rectKey{t.TilesetUid, t.X, t.Y, t.W, t.H} },
			put: func(x, y int64) { t.X, t.Y = x, y },
		})
	}
	fromMap := func(m map[string]interface{}) {
		num := func(k string) int64 { f, _ := m[k].(float64); return int64(f) }
		if _, ok := m["tilesetUid"]; !ok {
			return
		}
		out = append(out, rectRef{
			k:   func() rectKey { return rectKey{num("tilesetUid"), num("x"), num("y"), num("w"), num("h")} },
			put: func(x, y int64) { m["x"], m["y"] = float64(x), float64(y) },
		})
	}
	fromFields := func(fields []ldtk.FieldInstance) {
		for i := range fields {
			f := &fields[i]
			fromRect(f.Tile)
			switch v := f.Value.(type) {
			case map[string]interface{}:
				if f.Type == "Tile" {
					fromMap(v)
				}
			case []interface{}:
				if f.Type == "Array<Tile>" {
					for _, el := range v {
						if m, ok := el.(map[string]interface{}); ok {
							fromMap(m)
						}
					}
				}
			}
		}
	}
	for li := range l.Levels {
		for yi := range l.Levels[li].LayerInstances {
			layer := &l.Levels[li].LayerInstances[yi]
			for ei := range layer.EntityInstances {
				e := &layer.EntityInstances[ei]
				fromRect(e.Tile)
				fromFields(e.FieldInstances)
			}
		}
	}
	return out
}
