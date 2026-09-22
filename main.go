package main

import (
	"flag"
	"fmt"
	"image/png"
	"os"
	"path/filepath"

	"github.com/gaboose/ldtkbuddy/ldtkslim"
)

var outDir = flag.String("o", "out", "output directory")

func outputPath(filename string) string {
	return filepath.Join(*outDir, filename)
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: ldtkbuddy [-o dir] level.ldtk\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	ldtkPath := flag.Arg(0)
	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0755); err != nil {
			panic(err)
		}
	}

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

	if err := os.WriteFile(outputPath(filepath.Base(ldtkPath)), bts, 0644); err != nil {
		panic(err)
	}

	for p, img := range ts.Tilesets {
		func() {
			f, err := os.Create(outputPath(p))
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
