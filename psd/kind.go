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

// true when a naive render of n can differ from Photoshop: special layers,
// clipping, non-normal blend modes, layer effects, fill opacity, vector masks,
// and group masks or opacity.
func (n *Node) RenderApprox() bool {
	if n.Rec != nil && (n.Special() != "" || n.Clipped() || !n.normalBlend() || n.unrenderedStyle()) {
		return true
	}
	for _, c := range n.Children {
		if !c.Hidden() && c.RenderApprox() {
			return true
		}
	}
	return false
}

// groups store their real blend mode in the section divider (the record says "norm" for pass-through).
func (n *Node) normalBlend() bool {
	k := string(n.Rec.BlendKey[:])
	if b := n.Rec.block("lsct"); n.IsGroup() && b != nil && len(b.Data) >= 12 {
		k = string(b.Data[8:12])
	}
	return k == "norm" || (n.IsGroup() && k == "pass")
}

var unrenderedKeys = []string{"lfx2", "lrFX", "lmfx", "vmsk", "vsms"}

// styling the naive renderer ignores.
func (n *Node) unrenderedStyle() bool {
	r := n.Rec
	for _, k := range unrenderedKeys {
		if r.block(k) != nil {
			return true
		}
	}
	if b := r.block("iOpa"); b != nil && len(b.Data) > 0 && b.Data[0] != 255 {
		return true
	}
	if b := r.block("knko"); b != nil && len(b.Data) > 0 && b.Data[0] != 0 {
		return true
	}
	// enabled user mask with density or feather parameters
	if m := r.Mask; len(m) >= 19 && m[17]&0x12 == 0x10 && m[18]&0x03 != 0 {
		return true
	}
	// blend-if sliders moved off black 0-0 / white 255-255
	for i := 0; i+4 <= len(r.Ranges); i += 4 {
		if string(r.Ranges[i:i+4]) != "\x00\x00\xff\xff" {
			return true
		}
	}
	return n.IsGroup() && (n.HasMask() || r.Opacity != 255)
}
