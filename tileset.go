package main

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"

	"github.com/gaboose/ldtkbuddy/ldtk"
)

// TilesetShrinker rewrites an LDtk project and its tileset atlases so that the
// atlases only contain the tiles the project actually uses.
//
// Only tilesets whose image sits inside the project directory (relPath is
// local, file exists, no padding/spacing) are touched. Everything else is left
// exactly as it was.
//
// A "used" thing is either a single tile placed on a tile/auto layer, a
// pixel rectangle referenced from an entity (its __tile or any Tile / Array<Tile>
// field), or an animation offset referenced in custom data. A rectangle is moved
// as one block so its pixels stay contiguous. Editor-only definitions (entity
// def tile rects etc.) are not considered.
type TilesetShrinker struct {
	LDtk ldtk.LDtk

	// Tilesets maps a path relative to the ldtk file to its image. It holds
	// the atlases themselves and the collision images found next to them
	// (atlas1.png -> atlas2.png, atlas3.png, ...). Collision images must
	// have the same size and layout as their atlas and are shrunk identically.
	// After Shrink the images are the shrunk ones; the caller saves them.
	Tilesets map[string]image.Image

	atlases map[int64]*atlas // by tileset uid
}

func NewTilesetShrinker(ldtkPath string) (TilesetShrinker, error) {
	data, err := os.ReadFile(ldtkPath)
	if err != nil {
		return TilesetShrinker{}, err
	}
	project, err := ldtk.UnmarshalLDtk(data)
	if err != nil {
		return TilesetShrinker{}, fmt.Errorf("%s: %w", ldtkPath, err)
	}
	if project.ExternalLevels {
		return TilesetShrinker{}, errors.New("projects with external levels are not supported")
	}

	ts := TilesetShrinker{
		LDtk:     project,
		Tilesets: map[string]image.Image{},
		atlases:  map[int64]*atlas{},
	}
	dir := filepath.Dir(ldtkPath)
	for i := range ts.LDtk.Defs.Tilesets {
		def := &ts.LDtk.Defs.Tilesets[i]
		a, err := loadAtlas(dir, i, def, ts.Tilesets)
		if err != nil {
			return TilesetShrinker{}, err
		}
		if a != nil {
			ts.atlases[def.Uid] = a
		}
	}
	return ts, nil
}

func (ts *TilesetShrinker) Shrink() error {
	// 1. Find all the tiles used by the ldtk file
	usages := ts.collectUsage()

	// 2. Create a layout transformation data structure that maps old places of the tile to new ones
	layouts := map[int64]*layout{}
	for uid, a := range ts.atlases {
		def := &ts.LDtk.Defs.Tilesets[a.Index]
		l, err := planLayout(def, usages[uid])
		if err != nil {
			return fmt.Errorf("tileset %q: %w", def.Identifier, err)
		}
		if l != nil { // nil: nothing used, leave the tileset alone
			layouts[uid] = l
		}
	}

	// 3. Apply the layout transformation to ldtk and the images.
	if err := ts.applyToLDtk(layouts); err != nil {
		return err
	}
	ts.applyToImages(layouts)
	return nil
}
