package perplexity

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func attachmentTestPNG(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	require.NoError(t, png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	return buffer.Bytes()
}

func attachmentTestDataURL(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func attachmentTestChat(t *testing.T, parts ...any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": "sonar", "messages": []any{map[string]any{"role": "user", "content": parts}}})
	require.NoError(t, err)
	return body
}

func attachmentTestImagePart(url string) map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
}

func TestPrepareImageAttachmentsPNGJPEGWebP(t *testing.T) {
	var jpegBuffer bytes.Buffer
	require.NoError(t, jpeg.Encode(&jpegBuffer, image.NewRGBA(image.Rect(0, 0, 3, 2)), nil))
	// A complete 1x1 VP8 WebP fixture; DecodeConfig comes from the existing x/image dependency.
	webpBytes, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	require.NoError(t, err)
	for _, tt := range []struct {
		name string
		mime string
		data []byte
		ext  string
	}{
		{name: "PNG", mime: "image/png", data: attachmentTestPNG(t), ext: ".png"},
		{name: "JPEG", mime: "image/jpeg", data: jpegBuffer.Bytes(), ext: ".jpg"},
		{name: "WebP", mime: "image/webp", data: webpBytes, ext: ".webp"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := attachmentTestChat(t, attachmentTestImagePart(attachmentTestDataURL(tt.mime, tt.data)))
			sanitized, images, err := PrepareImageAttachments(body)
			require.NoError(t, err)
			require.Len(t, images, 1)
			require.Equal(t, tt.mime, images[0].ContentType)
			require.Equal(t, tt.data, images[0].Data)
			require.Equal(t, "image-1"+tt.ext, images[0].Filename)
			require.NotContains(t, string(sanitized), "base64")
			require.Equal(t, "text", gjson.GetBytes(sanitized, "messages.0.content.0.type").String())
			query, err := FoldMessagesToQuery(gjson.GetBytes(sanitized, "messages"))
			require.NoError(t, err, "image-only content must become a usable query")
			require.Equal(t, "[Image attachment 1]", query)
		})
	}
}

func TestPrepareImageAttachmentsPreservesTextAndUnchangedBody(t *testing.T) {
	plain := []byte(`{ "messages": [{"role":"user","content":"hello"}], "model":"sonar" }`)
	sanitized, images, err := PrepareImageAttachments(plain)
	require.NoError(t, err)
	require.Equal(t, plain, sanitized)
	require.Nil(t, images)

	body := attachmentTestChat(t,
		map[string]any{"type": "text", "text": "Compare <first> & second:"},
		attachmentTestImagePart(attachmentTestDataURL("image/png", attachmentTestPNG(t))),
		map[string]any{"type": "input_image", "image_url": attachmentTestDataURL("image/png", attachmentTestPNG(t))},
	)
	sanitized, images, err = PrepareImageAttachments(body)
	require.NoError(t, err)
	require.Len(t, images, 2)
	require.Equal(t, "Compare <first> & second:", gjson.GetBytes(sanitized, "messages.0.content.0.text").String())
	query, err := FoldMessagesToQuery(gjson.GetBytes(sanitized, "messages"))
	require.NoError(t, err)
	require.Contains(t, query, "[Image attachment 1]")
	require.Contains(t, query, "[Image attachment 2]")
}

func TestPrepareImageAttachmentsRejectsUnsafeInput(t *testing.T) {
	pngData := attachmentTestPNG(t)
	validURL := attachmentTestDataURL("image/png", pngData)
	tooWide := bytes.Clone(pngData)
	binary.BigEndian.PutUint32(tooWide[16:20], MaxImageDimension+1)
	binary.BigEndian.PutUint32(tooWide[29:33], crc32.ChecksumIEEE(tooWide[12:29]))
	tooManyPixels := bytes.Clone(pngData)
	binary.BigEndian.PutUint32(tooManyPixels[16:20], 10000)
	binary.BigEndian.PutUint32(tooManyPixels[20:24], 10000)
	binary.BigEndian.PutUint32(tooManyPixels[29:33], crc32.ChecksumIEEE(tooManyPixels[12:29]))

	cases := []struct {
		name string
		part any
	}{
		{name: "remote URL", part: attachmentTestImagePart("https://127.0.0.1/private.png")},
		{name: "public remote URL", part: attachmentTestImagePart("https://example.com/photo.png")},
		{name: "file URL", part: attachmentTestImagePart("file:///etc/passwd")},
		{name: "missing URL", part: map[string]any{"type": "image_url"}},
		{name: "non-string URL", part: map[string]any{"type": "image_url", "image_url": map[string]any{"url": true}}},
		{name: "malformed base64", part: attachmentTestImagePart("data:image/png;base64,!!!!")},
		{name: "noncanonical pad bits", part: attachmentTestImagePart("data:image/png;base64,AB==")},
		{name: "whitespace base64", part: attachmentTestImagePart(validURL + "\n")},
		{name: "unencoded data", part: attachmentTestImagePart("data:image/png,anything")},
		{name: "empty data", part: attachmentTestImagePart("data:image/png;base64,")},
		{name: "unsupported MIME", part: attachmentTestImagePart(attachmentTestDataURL("image/gif", pngData))},
		{name: "MIME mismatch", part: attachmentTestImagePart(attachmentTestDataURL("image/jpeg", pngData))},
		{name: "magic but invalid config", part: attachmentTestImagePart(attachmentTestDataURL("image/png", []byte("\x89PNG\r\n\x1a\ninvalid")))},
		{name: "excess width", part: attachmentTestImagePart(attachmentTestDataURL("image/png", tooWide))},
		{name: "excess pixels", part: attachmentTestImagePart(attachmentTestDataURL("image/png", tooManyPixels))},
		{name: "image file ID", part: map[string]any{"type": "input_image", "file_id": "file-secret"}},
		{name: "nested file ID", part: map[string]any{"type": "image_url", "image_url": map[string]any{"url": validURL, "file_id": "file-secret"}}},
		{name: "file", part: map[string]any{"type": "file", "file": map[string]any{"file_data": validURL}}},
		{name: "document", part: map[string]any{"type": "document", "source": map[string]any{"url": "https://example.com/a.pdf"}}},
		{name: "unknown image part", part: map[string]any{"type": "custom_image", "image_url": validURL}},
		{name: "null part", part: nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			body := attachmentTestChat(t, attachmentTestImagePart(validURL), tt.part)
			sanitized, images, err := PrepareImageAttachments(body)
			require.Error(t, err)
			require.Nil(t, sanitized, "no partially sanitized body may escape")
			require.Nil(t, images, "no partially validated image list may escape")
		})
	}
}

func TestPrepareImageAttachmentsCountAndAggregateLimits(t *testing.T) {
	pngData := attachmentTestPNG(t)
	part := attachmentTestImagePart(attachmentTestDataURL("image/png", pngData))
	five := []any{part, part, part, part, part}
	_, images, err := PrepareImageAttachments(attachmentTestChat(t, five...))
	require.NoError(t, err)
	require.Len(t, images, MaxImageAttachments)
	_, _, err = PrepareImageAttachments(attachmentTestChat(t, append(five, part)...))
	require.ErrorContains(t, err, "at most 5")

	// Header parsing is bounded and does not decode a raster into memory.
	padded := make([]byte, MaxImageAttachmentBytes)
	copy(padded, pngData)
	largeURL := attachmentTestDataURL("image/png", padded)
	_, images, err = PrepareImageAttachments(attachmentTestChat(t, attachmentTestImagePart(largeURL)))
	require.NoError(t, err, "the exact 20 MiB boundary is supported")
	require.Len(t, images, 1)
	_, _, err = PrepareImageAttachments(attachmentTestChat(t, part, attachmentTestImagePart(largeURL)))
	require.ErrorContains(t, err, "aggregate 20 MiB")
	_, _, err = PrepareImageAttachments(attachmentTestChat(t, attachmentTestImagePart("data:image/png;base64,"+strings.Repeat("A", 4*((MaxImageAttachmentBytes+2)/3)+4))))
	require.ErrorContains(t, err, "aggregate 20 MiB")
}

func attachmentTestMetadata(t *testing.T, bucketURL, objectURL string, fields map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"s3_bucket_url": bucketURL, "s3_object_url": objectURL, "fields": fields})
	require.NoError(t, err)
	return body
}

func TestParseImageUploadMetadataSupportedS3Forms(t *testing.T) {
	image := ImageAttachment{Filename: "image-1.png", ContentType: "image/png", Data: []byte("test")}
	for _, tt := range []struct {
		name, bucket, object string
	}{
		{name: "virtual hosted", bucket: "https://pplx-fixture.s3.amazonaws.com", object: "https://pplx-fixture.s3.amazonaws.com/user_uploads/image-1.png"},
		{name: "regional", bucket: "https://pplx-fixture.s3.us-west-2.amazonaws.com/", object: "https://pplx-fixture.s3.us-west-2.amazonaws.com/user_uploads/image-1.png"},
		{name: "legacy regional", bucket: "https://pplx-fixture.s3-us-west-2.amazonaws.com/", object: "https://pplx-fixture.s3-us-west-2.amazonaws.com/user_uploads/image-1.png"},
		{name: "path style", bucket: "https://s3.amazonaws.com/pplx-fixture/", object: "https://s3.amazonaws.com/pplx-fixture/user_uploads/image-1.png"},
		{name: "HTTPS443", bucket: "https://pplx-fixture.s3.amazonaws.com:443/", object: "https://pplx-fixture.s3.amazonaws.com:443/user_uploads/image-1.png"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := attachmentTestMetadata(t, tt.bucket, tt.object, map[string]any{"key": "user_uploads/${filename}", "policy": "signed-policy", "success_action_status": 201, "Content-Type": "image/png"})
			metadata, err := ParseImageUploadMetadata(body, image)
			require.NoError(t, err)
			require.Equal(t, tt.bucket, metadata.BucketURL)
			require.Equal(t, tt.object, metadata.ObjectURL)
			require.Equal(t, "201", metadata.Fields["success_action_status"])
			require.Equal(t, "user_uploads/${filename}", metadata.Fields["key"], "signed fields must not be rewritten")
		})
	}
	body, err := BuildImageUploadAuthorization(image)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Equal(t, map[string]any{"content_type": "image/png", "file_size": float64(4), "filename": "image-1.png", "force_image": false, "source": "default"}, payload)
}

func TestParseImageUploadMetadataRejectsUntrustedDestinationsAndForms(t *testing.T) {
	image := ImageAttachment{Filename: "image-1.png", ContentType: "image/png"}
	bucket := "https://pplx-fixture.s3.amazonaws.com/"
	object := bucket + "user_uploads/image-1.png"
	for _, tt := range []struct {
		name, bucket, object string
	}{
		{name: "non-S3 AWS", bucket: "https://lambda.us-west-2.amazonaws.com/", object: object},
		{name: "evil suffix", bucket: "https://pplx-fixture.s3.amazonaws.com.evil.test/", object: object},
		{name: "evil prefix", bucket: "https://not-s3.amazonaws.com/", object: object},
		{name: "cloudinary unsupported", bucket: "https://api.cloudinary.com/v1_1/test/image/upload", object: object},
		{name: "HTTP", bucket: strings.Replace(bucket, "https:", "http:", 1), object: object},
		{name: "userinfo", bucket: "https://secret@pplx-fixture.s3.amazonaws.com/", object: object},
		{name: "wrong port", bucket: "https://pplx-fixture.s3.amazonaws.com:8443/", object: object},
		{name: "empty port", bucket: "https://pplx-fixture.s3.amazonaws.com:/", object: object},
		{name: "private IP", bucket: "https://127.0.0.1/", object: object},
		{name: "IPv6", bucket: "https://[::1]/", object: object},
		{name: "bucket query", bucket: bucket + "?redirect=evil", object: object},
		{name: "bucket fragment", bucket: bucket + "#evil", object: object},
		{name: "untrusted object", bucket: bucket, object: "https://evil.test/user_uploads/image-1.png"},
		{name: "different bucket", bucket: bucket, object: "https://different.s3.amazonaws.com/user_uploads/image-1.png"},
		{name: "wrong prefix", bucket: bucket, object: bucket + "other/image-1.png"},
		{name: "traversal", bucket: bucket, object: bucket + "user_uploads/../image-1.png"},
		{name: "encoded traversal", bucket: bucket, object: bucket + "user_uploads/%2e%2e/image-1.png"},
		{name: "backslash", bucket: bucket, object: bucket + "user_uploads%5cimage-1.png"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseImageUploadMetadata(attachmentTestMetadata(t, tt.bucket, tt.object, map[string]any{"key": "user_uploads/image-1.png"}), image)
			require.Error(t, err)
		})
	}
	for _, tt := range []struct {
		name   string
		fields map[string]any
	}{
		{name: "empty", fields: map[string]any{}},
		{name: "missing key", fields: map[string]any{"policy": "signed"}},
		{name: "file collision", fields: map[string]any{"key": "user_uploads/image-1.png", "file": "malicious"}},
		{name: "cookie form field", fields: map[string]any{"key": "user_uploads/image-1.png", "Cookie": "must-not-leave"}},
		{name: "authorization form field", fields: map[string]any{"key": "user_uploads/image-1.png", "Authorization": "must-not-leave"}},
		{name: "redirect", fields: map[string]any{"key": "user_uploads/image-1.png", "success_action_redirect": "https://evil.test"}},
		{name: "boolean", fields: map[string]any{"key": "user_uploads/image-1.png", "policy": true}},
		{name: "null", fields: map[string]any{"key": "user_uploads/image-1.png", "policy": nil}},
		{name: "decimal", fields: map[string]any{"key": "user_uploads/image-1.png", "policy": 1.5}},
		{name: "nested", fields: map[string]any{"key": "user_uploads/image-1.png", "policy": map[string]string{}}},
		{name: "header injection", fields: map[string]any{"key": "user_uploads/image-1.png", "evil\r\nX-Auth": "secret"}},
		{name: "control field name", fields: map[string]any{"key": "user_uploads/image-1.png", "evil\tfield": "secret"}},
		{name: "MIME mismatch", fields: map[string]any{"key": "user_uploads/image-1.png", "Content-Type": "application/pdf"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseImageUploadMetadata(attachmentTestMetadata(t, bucket, object, tt.fields), image)
			require.Error(t, err)
		})
	}
	for _, raw := range []string{`{"error":"no uploads"}`, `{"rate_limited":true}`, `{"upload_url":"https://example.com/document","file_uuid":"doc"}`, `{`, `null`, strings.Repeat(" ", MaxUploadMetadataBytes+1)} {
		_, err := ParseImageUploadMetadata([]byte(raw), image)
		require.Error(t, err)
	}
}
