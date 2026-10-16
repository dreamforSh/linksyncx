package perplexity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/image/webp"
)

// Image attachment limits apply to the entire request, including historical turns.
const (
	MaxImageAttachments     = 5
	MaxImageAttachmentBytes = 20 << 20
	MaxImageDimension       = 16384
	MaxImagePixels          = 40_000_000
	MaxUploadMetadataBytes  = 128 << 10

	// ImageUploadAuthorizationPath matches the upload protocol in the MIT reference.
	ImageUploadAuthorizationPath = "/rest/uploads/create_upload_url?version=" + askVersion + "&source=default"
)

// ImageAttachment is a validated, bounded inline image. Filenames are generated
// locally rather than accepted from an untrusted content part.
type ImageAttachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// PrepareImageAttachments validates all content before the caller may make any
// outbound request. It replaces each inline image with an explicit numbered text
// placeholder, so image-only queries remain meaningful when folded to query_str.
// Remote images, stored file IDs, documents, and other media are not supported.
func PrepareImageAttachments(chatBody []byte) ([]byte, []ImageAttachment, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(chatBody, &root); err != nil || root == nil {
		return nil, nil, fmt.Errorf("invalid chat request JSON")
	}
	for _, key := range []string{"attachments", "files", "file", "file_id", "file_data", "file_url", "image_url", "image_file"} {
		if value, ok := root[key]; ok && string(value) != "null" && string(value) != "[]" {
			return nil, nil, fmt.Errorf("perplexity only supports inline PNG, JPEG, or WebP image content parts; %s is unsupported", key)
		}
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(root["messages"], &messages); err != nil || len(messages) == 0 {
		return nil, nil, fmt.Errorf("messages must be a non-empty array of objects")
	}

	var attachments []ImageAttachment
	totalBytes := 0
	for messageIndex, message := range messages {
		if message == nil {
			return nil, nil, fmt.Errorf("messages[%d] must be an object", messageIndex)
		}
		for _, key := range []string{"attachments", "files", "file", "file_id", "file_data", "file_url", "image_url", "image_file"} {
			if value, ok := message[key]; ok && string(value) != "null" && string(value) != "[]" {
				return nil, nil, fmt.Errorf("message-level %s attachments are unsupported; use inline image_url content parts", key)
			}
		}
		content := bytes.TrimSpace(message["content"])
		if len(content) == 0 || bytes.Equal(content, []byte("null")) || content[0] == '"' {
			continue
		}
		var parts []map[string]json.RawMessage
		if err := json.Unmarshal(content, &parts); err != nil || len(content) == 0 || content[0] != '[' {
			return nil, nil, fmt.Errorf("messages[%d].content must be text or an array of content parts", messageIndex)
		}
		changed := false
		for partIndex, part := range parts {
			if part == nil {
				return nil, nil, fmt.Errorf("messages[%d].content[%d] must be an object", messageIndex, partIndex)
			}
			var kind string
			if err := json.Unmarshal(part["type"], &kind); err != nil {
				return nil, nil, fmt.Errorf("content part type must be a string")
			}
			for _, key := range []string{"file_id", "file_data", "file_url", "file", "image_file"} {
				if _, ok := part[key]; ok {
					return nil, nil, fmt.Errorf("perplexity does not support documents or file_id attachments")
				}
			}
			switch kind {
			case "image_url", "input_image":
				if len(attachments) >= MaxImageAttachments {
					return nil, nil, fmt.Errorf("a request may contain at most %d images", MaxImageAttachments)
				}
				value, err := imageDataURL(part["image_url"])
				if err != nil {
					return nil, nil, err
				}
				attachment, err := decodeInlineImage(value, len(attachments)+1, MaxImageAttachmentBytes-totalBytes)
				if err != nil {
					return nil, nil, err
				}
				totalBytes += len(attachment.Data)
				attachments = append(attachments, attachment)
				text, err := json.Marshal(fmt.Sprintf("\n[Image attachment %d]\n", len(attachments)))
				if err != nil {
					return nil, nil, fmt.Errorf("encode image placeholder: %w", err)
				}
				parts[partIndex] = map[string]json.RawMessage{"type": json.RawMessage(`"text"`), "text": text}
				changed = true
			case "file", "input_file", "output_file", "document", "input_document", "image", "image_file", "audio", "input_audio", "audio_url", "video", "input_video", "video_url":
				return nil, nil, fmt.Errorf("unsupported Perplexity content part %q; use inline PNG, JPEG, or WebP image_url data URLs", kind)
			default:
				if _, ok := part["image_url"]; ok {
					return nil, nil, fmt.Errorf("unsupported image content part %q", kind)
				}
			}
		}
		if changed {
			encoded, err := json.Marshal(parts)
			if err != nil {
				return nil, nil, fmt.Errorf("encode sanitized image content: %w", err)
			}
			message["content"] = encoded
		}
	}
	if len(attachments) == 0 {
		return chatBody, nil, nil
	}
	encodedMessages, err := json.Marshal(messages)
	if err != nil {
		return nil, nil, fmt.Errorf("encode sanitized messages: %w", err)
	}
	root["messages"] = encodedMessages
	sanitized, err := json.Marshal(root)
	if err != nil {
		return nil, nil, fmt.Errorf("encode sanitized chat request: %w", err)
	}
	return sanitized, attachments, nil
}

func imageDataURL(raw json.RawMessage) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return "", fmt.Errorf("image_url must be a base64 data URL or an object containing url")
	}
	if _, ok := object["file_id"]; ok {
		return "", fmt.Errorf("perplexity does not support file_id attachments")
	}
	if err := json.Unmarshal(object["url"], &value); err != nil {
		return "", fmt.Errorf("image_url.url must be a base64 data URL")
	}
	return value, nil
}

func decodeInlineImage(value string, index, remainingBytes int) (ImageAttachment, error) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
		return ImageAttachment{}, fmt.Errorf("only base64 data:image/png, data:image/jpeg, or data:image/webp URLs are supported; remote image URLs are unsupported")
	}
	contentType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	var extension string
	var decodeConfig func(io.Reader) (image.Config, error)
	switch contentType {
	case "image/png":
		extension = ".png"
		decodeConfig = png.DecodeConfig
	case "image/jpeg":
		extension = ".jpg"
		decodeConfig = jpeg.DecodeConfig
	case "image/webp":
		extension = ".webp"
		decodeConfig = webp.DecodeConfig
	default:
		return ImageAttachment{}, fmt.Errorf("unsupported inline image MIME type; only PNG, JPEG, and WebP are supported")
	}
	if encoded == "" {
		return ImageAttachment{}, fmt.Errorf("inline image data is empty")
	}
	if len(encoded) > 4*((remainingBytes+2)/3) {
		return ImageAttachment{}, fmt.Errorf("new image data exceeds the aggregate 20 MiB limit")
	}
	// Go's Strict decoder still ignores CR/LF. Reject all non-alphabet bytes
	// explicitly to enforce canonical, whitespace-free standard base64.
	for i := range len(encoded) {
		b := encoded[i]
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '+' || b == '/' || b == '=' {
			continue
		}
		return ImageAttachment{}, fmt.Errorf("inline image contains invalid base64 data")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return ImageAttachment{}, fmt.Errorf("inline image contains invalid base64 data")
	}
	if len(data) == 0 || len(data) > remainingBytes {
		return ImageAttachment{}, fmt.Errorf("new image data exceeds the aggregate 20 MiB limit")
	}
	matchesMagic := false
	switch contentType {
	case "image/png":
		matchesMagic = bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n"))
	case "image/jpeg":
		matchesMagic = bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff})
	case "image/webp":
		matchesMagic = len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
	}
	if !matchesMagic {
		return ImageAttachment{}, fmt.Errorf("image content does not match its declared MIME type")
	}
	config, err := decodeConfig(bytes.NewReader(data))
	if err != nil {
		return ImageAttachment{}, fmt.Errorf("inline image has an invalid %s header", contentType)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > MaxImageDimension || config.Height > MaxImageDimension || int64(config.Width)*int64(config.Height) > MaxImagePixels {
		return ImageAttachment{}, fmt.Errorf("image dimensions exceed the %d-pixel edge or %d-pixel area limit", MaxImageDimension, MaxImagePixels)
	}
	return ImageAttachment{Filename: fmt.Sprintf("image-%d%s", index, extension), ContentType: contentType, Data: data}, nil
}

// BuildImageUploadAuthorization constructs only the reference's verified metadata
// request. force_image=false is intentional; images do not use document processing.
func BuildImageUploadAuthorization(attachment ImageAttachment) ([]byte, error) {
	return json.Marshal(struct {
		ContentType string `json:"content_type"`
		FileSize    int    `json:"file_size"`
		Filename    string `json:"filename"`
		ForceImage  bool   `json:"force_image"`
		Source      string `json:"source"`
	}{ContentType: attachment.ContentType, FileSize: len(attachment.Data), Filename: attachment.Filename, Source: "default"})
}

// ImageUploadMetadata is the supported S3 form-upload branch of create_upload_url.
// Cloudinary and document-processing protocols are deliberately not implemented.
type ImageUploadMetadata struct {
	BucketURL string
	ObjectURL string
	Fields    map[string]string
}

// ParseImageUploadMetadata validates the complete destination and form before any
// image bytes are sent. The object must be exactly the authorized bucket/key.
func ParseImageUploadMetadata(body []byte, attachment ImageAttachment) (ImageUploadMetadata, error) {
	if len(body) > MaxUploadMetadataBytes {
		return ImageUploadMetadata{}, fmt.Errorf("perplexity upload metadata exceeds its size limit")
	}
	var raw struct {
		BucketURL   string                     `json:"s3_bucket_url"`
		ObjectURL   string                     `json:"s3_object_url"`
		Fields      map[string]json.RawMessage `json:"fields"`
		Error       json.RawMessage            `json:"error"`
		RateLimited json.RawMessage            `json:"rate_limited"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return ImageUploadMetadata{}, fmt.Errorf("perplexity returned invalid image upload metadata")
	}
	for _, failure := range []json.RawMessage{raw.Error, raw.RateLimited} {
		if len(failure) != 0 && string(failure) != "null" && string(failure) != "false" && string(failure) != "\"\"" && string(failure) != "0" {
			return ImageUploadMetadata{}, fmt.Errorf("perplexity rejected image upload authorization or its quota is exhausted")
		}
	}
	if raw.BucketURL == "" || raw.ObjectURL == "" || len(raw.Fields) == 0 || len(raw.Fields) > 64 {
		return ImageUploadMetadata{}, fmt.Errorf("unsupported or incomplete Perplexity image upload metadata; an S3 form is required")
	}
	fields := make(map[string]string, len(raw.Fields))
	for name, value := range raw.Fields {
		if !validS3FormFieldName(name) {
			return ImageUploadMetadata{}, fmt.Errorf("perplexity returned an unsupported image upload form field")
		}
		var text string
		if len(value) > 0 && value[0] == '"' {
			if err := json.Unmarshal(value, &text); err != nil {
				return ImageUploadMetadata{}, fmt.Errorf("perplexity image upload form contains an invalid string")
			}
		} else {
			if _, err := strconv.ParseInt(string(value), 10, 64); err != nil {
				return ImageUploadMetadata{}, fmt.Errorf("perplexity image upload form values must be strings or integers")
			}
			text = string(value)
		}
		if len(text) > 64<<10 {
			return ImageUploadMetadata{}, fmt.Errorf("perplexity image upload form field exceeds its size limit")
		}
		if strings.EqualFold(name, "Content-Type") && text != attachment.ContentType {
			return ImageUploadMetadata{}, fmt.Errorf("perplexity image upload MIME type does not match the image")
		}
		fields[name] = text
	}
	bucket, err := parseS3URL(raw.BucketURL)
	if err != nil || bucket.RawQuery != "" {
		return ImageUploadMetadata{}, fmt.Errorf("perplexity returned an unsupported S3 upload destination")
	}
	object, err := parseS3URL(raw.ObjectURL)
	if err != nil || !strings.EqualFold(bucket.Hostname(), object.Hostname()) || bucket.Port() != object.Port() {
		return ImageUploadMetadata{}, fmt.Errorf("perplexity returned an untrusted S3 image object URL")
	}
	key := strings.ReplaceAll(fields["key"], "${filename}", attachment.Filename)
	if key == "" || len(key) > 4096 || !safeS3Path("/"+key) {
		return ImageUploadMetadata{}, fmt.Errorf("perplexity image upload form omitted a supported S3 object key")
	}
	prefix := strings.TrimSuffix(bucket.Path, "/")
	match := s3EndpointHost.FindStringSubmatch(strings.ToLower(bucket.Hostname()))
	if match[1] != "" {
		if prefix != "" {
			return ImageUploadMetadata{}, fmt.Errorf("unsupported S3 virtual-hosted bucket prefix")
		}
	} else if name := strings.TrimPrefix(prefix, "/"); !validS3BucketName(name) {
		return ImageUploadMetadata{}, fmt.Errorf("unsupported S3 path-style bucket prefix")
	}
	if object.Path != prefix+"/"+key {
		return ImageUploadMetadata{}, fmt.Errorf("perplexity image object URL does not match the authorized S3 bucket and key")
	}
	return ImageUploadMetadata{BucketURL: raw.BucketURL, ObjectURL: raw.ObjectURL, Fields: fields}, nil
}

var s3EndpointHost = regexp.MustCompile(`^(?:([a-z0-9][a-z0-9.-]{1,61}[a-z0-9])\.)?s3(?:[.-]([a-z]{2}(?:-[a-z]+)+-[0-9]+))?\.amazonaws\.com$`)

func parseS3URL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > 8192 || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\\\r\n\x00") {
		return nil, fmt.Errorf("invalid S3 URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.Hostname() == "" || (u.Port() != "" && u.Port() != "443") || net.ParseIP(u.Hostname()) != nil {
		return nil, fmt.Errorf("S3 URLs must use HTTPS port 443 without credentials or IP literals")
	}
	match := s3EndpointHost.FindStringSubmatch(strings.ToLower(u.Hostname()))
	if (u.Host != u.Hostname() && u.Host != u.Hostname()+":443") || len(match) == 0 || (match[1] != "" && !validS3BucketName(match[1])) || (u.Path != "" && u.Path != "/" && !safeS3Path(strings.TrimSuffix(u.Path, "/"))) {
		return nil, fmt.Errorf("unrecognized S3 endpoint or unsafe object path")
	}
	return u, nil
}

func validS3FormFieldName(name string) bool {
	switch strings.ToLower(name) {
	case "file", "success_action_redirect", "redirect", "cookie", "authorization", "proxy-authorization":
		return false
	}
	if name == "" || len(name) > 128 {
		return false
	}
	for i := range len(name) {
		b := name[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_' || b == '.' {
			continue
		}
		return false
	}
	return true
}

func validS3BucketName(name string) bool {
	if len(name) < 3 || len(name) > 63 || strings.Contains(name, "..") || net.ParseIP(name) != nil {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := range len(label) {
			b := label[i]
			if (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func safeS3Path(value string) bool {
	if value == "" || value[0] != '/' || path.Clean(value) != value || value == "/" {
		return false
	}
	for i := range len(value) {
		b := value[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || strings.ContainsRune("/-_.", rune(b)) {
			continue
		}
		return false
	}
	return true
}
