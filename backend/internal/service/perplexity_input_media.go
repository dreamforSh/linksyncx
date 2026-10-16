package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Validate the source shape before typed bridges can discard unsupported images.
// Full decoding and aggregate limits are enforced before any upload is started.
func validatePerplexityRawImage(part gjson.Result) error {
	switch part.Get("type").String() {
	case "image":
		source := part.Get("source")
		if source.Get("type").String() != "base64" || source.Get("data").Type != gjson.String {
			return perplexityOptionError("Perplexity supports only inline base64 PNG, JPEG, or WebP images")
		}
		switch source.Get("media_type").String() {
		case "image/png", "image/jpeg", "image/webp":
		default:
			return perplexityOptionError("Perplexity supports only inline base64 PNG, JPEG, or WebP images")
		}
	case "image_url", "input_image":
		if part.Get("file_id").Exists() || part.Get("image_file").Exists() {
			return perplexityOptionError("Perplexity does not support file_id image input")
		}
		value := part.Get("image_url")
		if value.IsObject() {
			value = value.Get("url")
		}
		if value.Type != gjson.String || !strings.HasPrefix(value.String(), "data:image/") {
			return perplexityOptionError("Perplexity supports only inline base64 image data URLs; remote images and file IDs are unsupported")
		}
	}
	return nil
}
