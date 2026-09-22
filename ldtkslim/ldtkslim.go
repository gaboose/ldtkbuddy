package ldtkslim

import (
	"encoding/json"
)

// Unmarshal json to ldtkslim.LDtk and marshal back into bytes.
// The effect is that only the fields of ldtkslim.LDtk are kept.
func Remarshal(data []byte) ([]byte, error) {
	var l LDtk
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, err
	}

	return json.Marshal(l)
}

type LDtk struct {
	Levels []Level `json:"levels"`
	Defs   Defs    `json:"defs"`
}

type Defs struct {
	Tilesets []Tileset `json:"tilesets"`
}

type Tileset struct {
	PxWid        int              `json:"pxWid"`
	TileGridSize int              `json:"tileGridSize"`
	EnumTags     []EnumTag        `json:"enumTags"`
	CustomData   []TileCustomData `json:"customData"`
}

type EnumTag struct {
	EnumValueID string `json:"enumValueId"`
	TileIDs     []int  `json:"tileIds"`
}

type TileCustomData struct {
	TileID int    `json:"tileId"`
	Data   string `json:"data"`
}

type Level struct {
	BgColor        string          `json:"__bgColor"`
	LayerInstances []LayerInstance `json:"layerInstances"`
}

type LayerInstance struct {
	Identifier      string           `json:"__identifier"`
	CWid            int              `json:"__cWid"`
	CHei            int              `json:"__cHei"`
	GridTiles       []Tile           `json:"gridTiles"`
	AutoLayerTiles  []Tile           `json:"autoLayerTiles"`
	EntityInstances []EntityInstance `json:"entityInstances"`
}

type Tile struct {
	T   int    `json:"t"`
	Px  [2]int `json:"px"`
	Src [2]int `json:"src"`
	F   int    `json:"f"`
}

type EntityInstance struct {
	Identifier string `json:"__identifier"`
	Px         [2]int `json:"px"`
}
