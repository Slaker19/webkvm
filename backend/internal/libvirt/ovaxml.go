package libvirt

import "encoding/xml"

// _xmlUnmarshal is the real encoding/xml.Unmarshal. Defined in its
// own file so the import list of ova.go stays small and intentional.
func _xmlUnmarshal(data []byte, v interface{}) error {
	return xml.Unmarshal(data, v)
}
