package main

// Plumbing for TilesetShrinker: loading, usage collection, layout planning
// and applying a layout. See tileset.go for the overview.

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/png"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/gaboose/ldtkbuddy/ldtk"
)

// ---- loading ----

type atlas struct {
	Index int      // index into LDtk.Defs.Tilesets
	Paths []string // keys of TilesetShrinker.Tilesets: atlas first, then collision images
}

// loadAtlas returns nil, nil when the tileset is not eligible for shrinking.
func loadAtlas(dir string, index int, def *ldtk.TilesetDefinition, images map[string]image.Image) (*atlas, error) {
	if def.RelPath == nil || def.Padding != 0 || def.Spacing != 0 || def.TileGridSize <= 0 {
		return nil, nil
	}
	rel := filepath.FromSlash(*def.RelPath)
	if !filepath.IsLocal(rel) {
		return nil, nil
	}

	a := &atlas{Index: index}
	for p, first := rel, true; ; first = false {
		img, err := readImage(filepath.Join(dir, p))
		if errors.Is(err, fs.ErrNotExist) {
			if first {
				return nil, nil
			}
			return a, nil
		}
		if err != nil {
			return nil, err
		}
		if b := img.Bounds(); b.Dx() != int(def.PxWid) || b.Dy() != int(def.PxHei) {
			return nil, fmt.Errorf("%s: is %dx%d, expected %dx%d like the atlas", p, b.Dx(), b.Dy(), def.PxWid, def.PxHei)
		}
		if _, dup := images[p]; dup {
			return nil, fmt.Errorf("%s: used by more than one tileset", p)
		}
		images[p] = img
		a.Paths = append(a.Paths, p)

		var ok bool
		if p, ok = nextNumbered(p); !ok {
			return a, nil
		}
	}
}

func readImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return img, nil
}

var numberedRe = regexp.MustCompile(`^(.*?)(\d+)(\.[^./\\]*)$`)

// nextNumbered turns "dir/atlas1.png" into "dir/atlas2.png".
func nextNumbered(p string) (string, bool) {
	m := numberedRe.FindStringSubmatch(p)
	if m == nil {
		return "", false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%s%0*d%s", m[1], len(m[2]), n+1, m[3]), true
}

// ---- finding references to tilesets inside the project ----

// pxRect is a pixel rectangle in a tileset image.
type pxRect struct {
	Uid        int64
	X, Y, W, H int
}

type usage struct {
	tiles map[int]bool // used tile ids
	rects []pxRect
}

func (ts *TilesetShrinker) collectUsage() map[int64]*usage {
	usages := map[int64]*usage{}
	for uid := range ts.atlases {
		usages[uid] = &usage{tiles: map[int]bool{}}
	}
	ts.walkReferences(
		func(uid int64, t *ldtk.TileInstance) {
			if u := usages[uid]; u != nil {
				u.tiles[int(t.T)] = true
			}
		},
		func(r *pxRect) {
			if u := usages[r.Uid]; u != nil {
				u.rects = append(u.rects, *r)
			}
		},
	)

	// Animation frames referenced from the custom data of used tiles.
	for uid, a := range ts.atlases {
		def := &ts.LDtk.Defs.Tilesets[a.Index]
		u := usages[uid]
		cols := int(def.PxWid) / int(def.TileGridSize)
		for _, c := range def.CustomData {
			if !u.tiles[int(c.TileID)] {
				continue
			}
			offsets, err := parseAnimationOffsets(c.Data)
			if err != nil {
				continue // reported by applyToDef
			}
			for _, o := range offsets {
				u.tiles[int(c.TileID)+o.X+o.Y*cols] = true
			}
		}
	}
	return usages
}

// Offset is a single animation frame offset in tile cells.
type Offset struct{ X, Y int }

const animationKey = "animationOffsets"

// parseAnimationOffsets reads the animationOffsets line of tile custom data.
func parseAnimationOffsets(data string) ([]Offset, error) {
	for _, line := range strings.Split(data, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), animationKey+" ")
		if !ok {
			continue
		}
		var pairs [][]int
		if err := json.Unmarshal([]byte("["+strings.TrimSpace(rest)+"]"), &pairs); err != nil {
			return nil, fmt.Errorf("parsing %s %q: %w", animationKey, rest, err)
		}
		offsets := make([]Offset, 0, len(pairs))
		for i, p := range pairs {
			if len(p) != 2 {
				return nil, fmt.Errorf("%s offset %d has %d values, want 2", animationKey, i, len(p))
			}
			offsets = append(offsets, Offset{p[0], p[1]})
		}
		return offsets, nil
	}
	return nil, nil
}

// replaceAnimationOffsets rewrites the animationOffsets line, keeping other lines.
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

// walkReferences calls onTile for every tile instance on tile layers and
// onRect for every tileset rectangle in entities, entity fields and level
// fields. Visitors may modify what they receive; for rects only X and Y are
// written back.
func (ts *TilesetShrinker) walkReferences(onTile func(uid int64, t *ldtk.TileInstance), onRect func(*pxRect)) {
	level := func(lv *ldtk.Level) {
		walkFields(lv.FieldInstances, onRect)
		for i := range lv.LayerInstances {
			li := &lv.LayerInstances[i]
			if li.TilesetDefUid != nil {
				for j := range li.GridTiles {
					onTile(*li.TilesetDefUid, &li.GridTiles[j])
				}
				for j := range li.AutoLayerTiles {
					onTile(*li.TilesetDefUid, &li.AutoLayerTiles[j])
				}
			}
			for j := range li.EntityInstances {
				e := &li.EntityInstances[j]
				walkRect(e.Tile, onRect)
				walkFields(e.FieldInstances, onRect)
			}
		}
	}
	for i := range ts.LDtk.Levels {
		level(&ts.LDtk.Levels[i])
	}
	for w := range ts.LDtk.Worlds {
		for i := range ts.LDtk.Worlds[w].Levels {
			level(&ts.LDtk.Worlds[w].Levels[i])
		}
	}
}

func walkRect(r *ldtk.TilesetRectangle, fn func(*pxRect)) {
	if r == nil {
		return
	}
	p := pxRect{r.TilesetUid, int(r.X), int(r.Y), int(r.W), int(r.H)}
	fn(&p)
	r.X, r.Y = int64(p.X), int64(p.Y)
}

func walkFields(fields []ldtk.FieldInstance, fn func(*pxRect)) {
	for i := range fields {
		walkRect(fields[i].Tile, fn)
		walkValue(fields[i].Value, fn)
	}
}

// walkValue finds Tile values ({tilesetUid,x,y,w,h}) in a decoded JSON field value.
func walkValue(v any, fn func(*pxRect)) {
	switch v := v.(type) {
	case []any:
		for _, e := range v {
			walkValue(e, fn)
		}
	case map[string]any:
		uid, ok0 := v["tilesetUid"].(float64)
		x, ok1 := v["x"].(float64)
		y, ok2 := v["y"].(float64)
		w, ok3 := v["w"].(float64)
		h, ok4 := v["h"].(float64)
		if !(ok0 && ok1 && ok2 && ok3 && ok4) {
			return
		}
		p := pxRect{int64(uid), int(x), int(y), int(w), int(h)}
		fn(&p)
		v["x"], v["y"] = float64(p.X), float64(p.Y)
	}
}

// ---- layout ----

// box is a rectangle of tile cells.
type box struct{ X, Y, W, H int }

func (a box) overlaps(b box) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func (a box) union(b box) box {
	x0, y0 := min(a.X, b.X), min(a.Y, b.Y)
	x1, y1 := max(a.X+a.W, b.X+b.W), max(a.Y+a.H, b.Y+b.H)
	return box{x0, y0, x1 - x0, y1 - y0}
}

// unit is a block of cells that moves as one.
type unit struct {
	Old        box
	NewX, NewY int // new top-left cell
}

// layout maps cells (and pixels) of an old atlas to a new, smaller atlas.
type layout struct {
	Grid             int
	OldCols, OldRows int
	NewCols, NewRows int
	Units            []unit
	unitAt           []int // old cell -> index into Units, or -1
}

// planLayout returns nil, nil if the tileset has no used tiles.
func planLayout(def *ldtk.TilesetDefinition, u *usage) (*layout, error) {
	g := int(def.TileGridSize)
	l := &layout{Grid: g, OldCols: int(def.PxWid) / g, OldRows: int(def.PxHei) / g}
	l.unitAt = make([]int, l.OldCols*l.OldRows)
	for i := range l.unitAt {
		l.unitAt[i] = -1
	}

	// Rectangles become cell boxes; overlapping ones are merged. Single tiles
	// can never grow a box, so they are added afterwards.
	var boxes []box
	for _, r := range u.rects {
		if r.W <= 0 || r.H <= 0 {
			continue
		}
		if r.X < 0 || r.Y < 0 || (r.X+r.W-1)/g >= l.OldCols || (r.Y+r.H-1)/g >= l.OldRows {
			return nil, fmt.Errorf("rect %+v is outside of the atlas", r)
		}
		x0, y0 := r.X/g, r.Y/g
		boxes = append(boxes, box{x0, y0, (r.X+r.W-1)/g - x0 + 1, (r.Y+r.H-1)/g - y0 + 1})
	}
	for _, b := range mergeBoxes(boxes) {
		l.addUnit(b)
	}
	for _, id := range slices.Sorted(mapKeys(u.tiles)) {
		if id < 0 || id >= l.OldCols*l.OldRows {
			return nil, fmt.Errorf("tile id %d is outside of the atlas", id)
		}
		if l.unitAt[id] < 0 {
			l.addUnit(box{id % l.OldCols, id / l.OldCols, 1, 1})
		}
	}
	if len(l.Units) == 0 {
		return nil, nil
	}
	l.pack()
	return l, nil
}

func mapKeys[K comparable, V any](m map[K]V) func(yield func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func (l *layout) addUnit(b box) {
	for y := b.Y; y < b.Y+b.H; y++ {
		for x := b.X; x < b.X+b.W; x++ {
			l.unitAt[y*l.OldCols+x] = len(l.Units)
		}
	}
	l.Units = append(l.Units, unit{Old: b})
}

func mergeBoxes(boxes []box) []box {
	for changed := true; changed; {
		changed = false
		for i := 0; i < len(boxes); i++ {
			for j := i + 1; j < len(boxes); j++ {
				if boxes[i].overlaps(boxes[j]) {
					boxes[i] = boxes[i].union(boxes[j])
					boxes = slices.Delete(boxes, j, j+1)
					j--
					changed = true
				}
			}
		}
	}
	return boxes
}

// pack places units on shelves, tallest first, in a roughly square atlas.
func (l *layout) pack() {
	area, maxW := 0, 0
	for _, u := range l.Units {
		area += u.Old.W * u.Old.H
		maxW = max(maxW, u.Old.W)
	}
	l.NewCols = min(l.OldCols, max(maxW, int(math.Ceil(math.Sqrt(float64(area))))))

	order := make([]int, len(l.Units))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(i, j int) int {
		a, b := l.Units[i].Old, l.Units[j].Old
		return cmp.Or(cmp.Compare(b.H, a.H), cmp.Compare(b.W, a.W), cmp.Compare(a.Y, b.Y), cmp.Compare(a.X, b.X))
	})

	x, y, shelf := 0, 0, 0
	for _, i := range order {
		u := &l.Units[i]
		if x+u.Old.W > l.NewCols {
			x, y, shelf = 0, y+shelf, 0
		}
		u.NewX, u.NewY = x, y
		x += u.Old.W
		shelf = max(shelf, u.Old.H)
	}
	l.NewRows = y + shelf
}

// MapPoint maps an old pixel position to the new one.
func (l *layout) MapPoint(x, y int) (nx, ny int, ok bool) {
	if x < 0 || y < 0 {
		return 0, 0, false
	}
	cx, cy := x/l.Grid, y/l.Grid
	if cx >= l.OldCols || cy >= l.OldRows {
		return 0, 0, false
	}
	i := l.unitAt[cy*l.OldCols+cx]
	if i < 0 {
		return 0, 0, false
	}
	u := l.Units[i]
	return x + (u.NewX-u.Old.X)*l.Grid, y + (u.NewY-u.Old.Y)*l.Grid, true
}

// MapTileID maps an old tile id to the new one.
func (l *layout) MapTileID(id int) (int, bool) {
	if id < 0 || id >= l.OldCols*l.OldRows {
		return 0, false
	}
	nx, ny, ok := l.MapPoint(id%l.OldCols*l.Grid, id/l.OldCols*l.Grid)
	if !ok {
		return 0, false
	}
	return ny/l.Grid*l.NewCols + nx/l.Grid, true
}

// ---- applying a layout ----

func (ts *TilesetShrinker) applyToLDtk(layouts map[int64]*layout) error {
	var firstErr error
	fail := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	ts.walkReferences(
		func(uid int64, t *ldtk.TileInstance) {
			l := layouts[uid]
			if l == nil {
				return
			}
			id, ok := l.MapTileID(int(t.T))
			if !ok {
				fail(fmt.Errorf("tile %d of tileset %d was not planned", t.T, uid))
				return
			}
			t.T = int64(id)
			t.Src = []int64{int64(id % l.NewCols * l.Grid), int64(id / l.NewCols * l.Grid)}
		},
		func(r *pxRect) {
			l := layouts[r.Uid]
			if l == nil || r.W <= 0 || r.H <= 0 {
				return
			}
			nx, ny, ok := l.MapPoint(r.X, r.Y)
			if !ok {
				fail(fmt.Errorf("rect %+v was not planned", *r))
				return
			}
			r.X, r.Y = nx, ny
		},
	)
	if firstErr != nil {
		return firstErr
	}

	for uid, l := range layouts {
		if err := l.applyToDef(&ts.LDtk.Defs.Tilesets[ts.atlases[uid].Index]); err != nil {
			return err
		}
	}
	return nil
}

func (l *layout) applyToDef(def *ldtk.TilesetDefinition) error {
	def.CWid, def.CHei = int64(l.NewCols), int64(l.NewRows)
	def.PxWid, def.PxHei = int64(l.NewCols*l.Grid), int64(l.NewRows*l.Grid)

	custom := make([]ldtk.TileCustomMetadata, 0, len(def.CustomData))
	for _, c := range def.CustomData {
		id, ok := l.MapTileID(int(c.TileID))
		if !ok {
			continue
		}
		data := c.Data
		offsets, err := parseAnimationOffsets(data)
		if err != nil {
			return fmt.Errorf("tileset %q tile %d: %w", def.Identifier, c.TileID, err)
		}
		if len(offsets) > 0 {
			// Frames are resolved against the old layout and re-expressed in the new one.
			for i, o := range offsets {
				ref, ok := l.MapTileID(int(c.TileID) + o.X + o.Y*l.OldCols)
				if !ok {
					return fmt.Errorf("tileset %q tile %d: animation frame was not planned", def.Identifier, c.TileID)
				}
				offsets[i] = Offset{ref%l.NewCols - id%l.NewCols, ref/l.NewCols - id/l.NewCols}
			}
			data = replaceAnimationOffsets(data, offsets)
		}
		custom = append(custom, ldtk.TileCustomMetadata{Data: data, TileID: int64(id)})
	}
	def.CustomData = custom

	for i := range def.EnumTags {
		ids := make([]int64, 0, len(def.EnumTags[i].TileIDS))
		for _, old := range def.EnumTags[i].TileIDS {
			if id, ok := l.MapTileID(int(old)); ok {
				ids = append(ids, int64(id))
			}
		}
		def.EnumTags[i].TileIDS = ids
	}

	// Stale after the move and only meaningful to the editor.
	def.CachedPixelData = nil
	def.SavedSelections = nil
	return nil
}

func (ts *TilesetShrinker) applyToImages(layouts map[int64]*layout) {
	for uid, l := range layouts {
		for _, p := range ts.atlases[uid].Paths {
			ts.Tilesets[p] = l.applyToImage(ts.Tilesets[p])
		}
	}
}

func (l *layout) applyToImage(src image.Image) image.Image {
	g := l.Grid
	dst := image.NewNRGBA(image.Rect(0, 0, l.NewCols*g, l.NewRows*g))
	for _, u := range l.Units {
		sr := image.Rect(u.Old.X*g, u.Old.Y*g, (u.Old.X+u.Old.W)*g, (u.Old.Y+u.Old.H)*g).Add(src.Bounds().Min)
		copyBlock(dst, image.Pt(u.NewX*g, u.NewY*g), src, sr)
	}
	return dst
}

// copyBlock copies sr from src to dst at dp. It is lossless for NRGBA sources.
func copyBlock(dst *image.NRGBA, dp image.Point, src image.Image, sr image.Rectangle) {
	if s, ok := src.(*image.NRGBA); ok {
		n := sr.Dx() * 4
		for y := 0; y < sr.Dy(); y++ {
			so := s.PixOffset(sr.Min.X, sr.Min.Y+y)
			do := dst.PixOffset(dp.X, dp.Y+y)
			copy(dst.Pix[do:do+n], s.Pix[so:so+n])
		}
		return
	}
	draw.Draw(dst, image.Rectangle{Min: dp, Max: dp.Add(sr.Size())}, src, sr.Min, draw.Src)
}
