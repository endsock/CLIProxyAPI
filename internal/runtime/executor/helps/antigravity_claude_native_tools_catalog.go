package helps

import (
	_ "embed"

	"github.com/tidwall/gjson"
)

//go:embed antigravity_claude_native_tools_catalog.json
var agyNativeToolsCatalogJSON []byte

//go:embed antigravity_claude_native_tools_system.json
var agyNativeSystemInstructionJSON []byte

var (
	agyNativeToolObjects             [][]byte
	agyNativeToolsCatalogArray       []byte
	agyNativeSystemInstructionObject []byte
)

func init() {
	tools := gjson.ParseBytes(agyNativeToolsCatalogJSON)
	if !tools.IsArray() {
		panic("helps: antigravity claude native tools catalog must be a JSON array")
	}
	objects := tools.Array()
	agyNativeToolObjects = make([][]byte, 0, len(objects))
	for _, tool := range objects {
		agyNativeToolObjects = append(agyNativeToolObjects, []byte(tool.Raw))
	}
	agyNativeToolsCatalogArray = []byte(tools.Raw)

	si := gjson.ParseBytes(agyNativeSystemInstructionJSON)
	if !si.IsObject() {
		panic("helps: antigravity claude native system instruction must be a JSON object")
	}
	agyNativeSystemInstructionObject = []byte(si.Raw)
}
