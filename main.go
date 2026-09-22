package main

import (
	"flag"
	"image/png"
	"os"
	"path"

	"github.com/gaboose/ldtkbuddy/ldtkslim"
)

func trimmedFilename(name string) string {
	ext := path.Ext(name)
	return name[:len(name)-len(ext)] + "_trimmed" + ext
}

func main() {
	flag.Parse()
	ldtkPath := flag.Arg(0)

	ts, err := NewTilesetShrinker(ldtkPath)
	if err != nil {
		panic(err)
	}

	if err := ts.Shrink(); err != nil {
		panic(err)
	}

	bts, err := ts.LDtk.Marshal()
	if err != nil {
		panic(err)
	}

	bts, err = ldtkslim.Remarshal(bts)
	if err != nil {
		panic(err)
	}

	if err := os.WriteFile(trimmedFilename(ldtkPath), bts, 0600); err != nil {
		panic(err)
	}

	ldtkDir := path.Dir(ldtkPath)
	for p, img := range ts.Tilesets {
		func() {
			f, err := os.Create(path.Join(ldtkDir, trimmedFilename(p)))
			if err != nil {
				panic(err)
			}
			defer f.Close()

			if err = png.Encode(f, img); err != nil {
				panic(err)
			}
		}()
	}
}
