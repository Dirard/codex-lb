package application

import "bytes"

// JSON prefilters skip a generic decode when the raw bytes cannot contain any
// marker the decoded walk looks for. Every marker is an ASCII fragment of the
// literal JSON text; a \u escape could spell a marker without its literal
// bytes, so it always forces the decode. A prefilter only answers "maybe": a
// hit falls back to the exact decoded result.
var (
	unicodeEscape = []byte(`\u`)
	// accountBoundMarkers: file-handle strings and keys (file_id, file_ids),
	// container_id, vector_store_id(s), input_audio call IDs, item_reference
	// and compaction items, and Lite additional_tools bundles.
	accountBoundMarkers = [][]byte{[]byte(`"file-`), []byte(`"file_`), []byte(`"container_id"`), []byte(`"vector_store_id`),
		[]byte(`input_audio`), []byte(`item_reference`), []byte(`"compaction"`), []byte(`additional_tools`)}
	// fileIDMarkers: the file_id and file_ids keys.
	fileIDMarkers = [][]byte{[]byte(`"file_id`)}
	// imageMarkers: input_image, image_url and image_generation items.
	imageMarkers = [][]byte{[]byte(`image`)}
)

func mayContainMarker(raw []byte, markers [][]byte) bool {
	if bytes.Contains(raw, unicodeEscape) {
		return true
	}
	for _, marker := range markers {
		if bytes.Contains(raw, marker) {
			return true
		}
	}
	return false
}
