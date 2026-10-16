package psd

// @region psd:kind -- LAYER CLASSIFICATION

var specialKeys = map[string]string{
	"TySh": "text", "tySh": "text",
	"SoLd": "smart object", "SoLE": "smart object", "PlLd": "smart object", "plLd": "smart object",
	"SoCo": "fill", "GdFl": "fill", "PtFl": "fill",
	"levl": "adjustment", "curv": "adjustment", "brit": "adjustment", "blnc": "adjustment",
	"hue ": "adjustment", "hue2": "adjustment", "selc": "adjustment", "mixr": "adjustment",
	"grdm": "adjustment", "phfl": "adjustment", "expA": "adjustment", "vibA": "adjustment",
	"thrs": "adjustment", "post": "adjustment", "nvrt": "adjustment", "blwh": "adjustment",
	"clrL": "adjustment", "CgEd": "adjustment",
}

// non-raster layer type ("text", "adjustment", ...) or "" for plain layers and groups.
func (n *Node) Special() string {
	if n.Rec == nil || n.IsGroup() {
		return ""
	}
	for _, b := range n.Rec.Blocks {
		if k, ok := specialKeys[b.Key]; ok {
			return k
		}
	}
	return ""
}

// true when the layer stores a user mask.
func (n *Node) HasMask() bool {
	return n.Rec != nil && len(n.Rec.Mask) > 0
}

func (n *Node) Clipped() bool {
	return n.Rec != nil && n.Rec.Clipping != 0
}

// true when n or any descendant is a special layer.
func (n *Node) ContainsSpecial() bool {
	if n.Special() != "" {
		return true
	}
	for _, c := range n.Children {
		if c.ContainsSpecial() {
			return true
		}
	}
	return false
}
