//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type perplexityAttachmentCall struct {
	request     *http.Request
	body        []byte
	proxy       string
	accountID   int64
	concurrency int
	profile     *tlsfingerprint.Profile
}

type perplexityAttachmentUpstream struct {
	t       *testing.T
	calls   []perplexityAttachmentCall
	respond func(int, *http.Request) (*http.Response, error)
}

func (u *perplexityAttachmentUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.t.Fatal("Perplexity attachment requests must preserve the TLS profile")
	return nil, errors.New("unexpected non-TLS request")
}

func (u *perplexityAttachmentUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.t.Helper()
	body, err := io.ReadAll(req.Body)
	require.NoError(u.t, err)
	u.calls = append(u.calls, perplexityAttachmentCall{request: req, body: body, proxy: proxy, accountID: accountID, concurrency: concurrency, profile: profile})
	// The fixture never resolves a hostname or opens a socket. Any accidental ask
	// or document-processing request is an immediate failure.
	require.NotContains(u.t, req.URL.Path, "perplexity_ask")
	require.NotContains(u.t, req.URL.Path, "attachment_processing")
	return u.respond(len(u.calls)-1, req)
}

func perplexityAttachmentResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func perplexityAttachmentPNG(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	require.NoError(t, png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	return buffer.Bytes()
}

func perplexityAttachmentURL(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func perplexityAttachmentPart(url string) map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
}

func perplexityAttachmentBody(t *testing.T, parts ...any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": "sonar", "messages": []any{map[string]any{"role": "user", "content": parts}}})
	require.NoError(t, err)
	return body
}

func perplexityAttachmentAccount() *Account {
	proxyID := int64(9)
	return &Account{
		ID: 42, Platform: PlatformPerplexity, Type: AccountTypeAPIKey, Concurrency: 3,
		ProxyID: &proxyID, Proxy: &Proxy{ID: proxyID, Protocol: "http", Host: "fixture-proxy.invalid", Port: 1234},
		Credentials: map[string]any{
			"api_key": "session-secret", "csrf_token": "csrf-secret", "base_url": "https://perplexity.fixture.invalid",
			"header_override_enabled": true,
			"header_overrides": map[string]any{
				"authorization": "Bearer override-secret", "x-account-secret": "never-send-to-S3", "user-agent": "fixture-account-agent",
			},
		},
	}
}

func perplexityAttachmentMetadata(t *testing.T, bucket, object string, fields map[string]any) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"s3_bucket_url": bucket, "s3_object_url": object, "fields": fields, "file_uuid": "not-a-document"})
	require.NoError(t, err)
	return string(body)
}

func TestPreparePerplexityImageAttachmentsMetadataMultipartAndProxy(t *testing.T) {
	imageData := perplexityAttachmentPNG(t)
	bucket := "https://pplx-fixture.s3.us-west-2.amazonaws.com/"
	object := bucket + "user_uploads/image-1.png"
	fields := map[string]any{
		"key": "user_uploads/image-1.png", "policy": "signed-policy", "x-amz-algorithm": "AWS4-HMAC-SHA256",
		"x-amz-credential": "fixture", "x-amz-signature": "signature", "Content-Type": "image/png", "success_action_status": 201,
	}
	upstream := &perplexityAttachmentUpstream{t: t}
	upstream.respond = func(index int, req *http.Request) (*http.Response, error) {
		switch index {
		case 0:
			require.Equal(t, "https://perplexity.fixture.invalid/rest/uploads/create_upload_url?version=2.18&source=default", req.URL.String())
			require.Equal(t, http.MethodPost, req.Method)
			require.Equal(t, "application/json", req.Header.Get("Content-Type"))
			require.Contains(t, req.Header.Get("Cookie"), "__Secure-next-auth.session-token=session-secret")
			require.Contains(t, req.Header.Get("Cookie"), "next-auth.csrf-token=csrf-secret")
			require.Equal(t, "https://perplexity.fixture.invalid", req.Header.Get("Origin"))
			require.Equal(t, "https://perplexity.fixture.invalid/", req.Header.Get("Referer"))
			require.Empty(t, req.Header.Get("Authorization"), "globally blocked credential overrides stay blocked")
			require.Equal(t, []string{"never-send-to-S3"}, req.Header["x-account-secret"], "eligible account overrides apply only at Perplexity")
			require.Equal(t, "fixture-account-agent", req.Header.Get("User-Agent"))
			var payload map[string]any
			require.NoError(t, json.Unmarshal(upstream.calls[index].body, &payload))
			require.Equal(t, map[string]any{
				"content_type": "image/png", "file_size": float64(len(imageData)), "filename": "image-1.png", "force_image": false, "source": "default",
			}, payload)
			return perplexityAttachmentResponse(200, perplexityAttachmentMetadata(t, bucket, object, fields)), nil
		case 1:
			require.Equal(t, bucket, req.URL.String())
			require.Equal(t, http.MethodPost, req.Method)
			require.True(t, HTTPUpstreamPublicHostsOnly(req.Context()))
			require.Empty(t, req.Header.Get("Cookie"))
			require.Empty(t, req.Header.Get("Authorization"))
			require.Empty(t, req.Header.Get("X-Account-Secret"))
			require.Empty(t, req.Header.Get("User-Agent"))
			require.Empty(t, req.Header.Get("Origin"))
			require.Empty(t, req.Header.Get("Referer"))
			for name := range req.Header {
				require.Contains(t, []string{"content-type", "accept"}, strings.ToLower(name), "no account header may reach S3 under any wire casing")
			}
			mediaType, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
			require.NoError(t, err)
			require.Equal(t, "multipart/form-data", mediaType)
			reader := multipart.NewReader(bytes.NewReader(upstream.calls[index].body), params["boundary"])
			actualFields := make(map[string]string)
			fileCount := 0
			for {
				part, err := reader.NextPart()
				if errors.Is(err, io.EOF) {
					break
				}
				require.NoError(t, err)
				data, err := io.ReadAll(part)
				require.NoError(t, err)
				if part.FormName() == "file" {
					fileCount++
					require.Equal(t, "image-1.png", part.FileName())
					require.Equal(t, "image/png", part.Header.Get("Content-Type"))
					require.Equal(t, imageData, data)
				} else {
					actualFields[part.FormName()] = string(data)
				}
			}
			require.Equal(t, 1, fileCount)
			require.Equal(t, map[string]string{
				"key": "user_uploads/image-1.png", "policy": "signed-policy", "x-amz-algorithm": "AWS4-HMAC-SHA256",
				"x-amz-credential": "fixture", "x-amz-signature": "signature", "Content-Type": "image/png", "success_action_status": "201",
			}, actualFields)
			return perplexityAttachmentResponse(201, ""), nil
		default:
			t.Fatalf("unexpected outbound request %d: %s", index, req.URL)
			return nil, nil
		}
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	ctx := WithHTTPUpstreamProfile(t.Context(), HTTPUpstreamProfileLongStream)
	sanitized, urls, err := svc.preparePerplexityImageAttachments(ctx, perplexityAttachmentAccount(), perplexityAttachmentBody(t, perplexityAttachmentPart(perplexityAttachmentURL("image/png", imageData))))
	require.NoError(t, err)
	require.Equal(t, []string{object}, urls)
	require.NotContains(t, string(sanitized), "base64")
	query, err := perplexity.FoldMessagesToQuery(gjson.GetBytes(sanitized, "messages"))
	require.NoError(t, err)
	require.Equal(t, "[Image attachment 1]", query)
	require.Len(t, upstream.calls, 2)
	for _, call := range upstream.calls {
		require.Equal(t, "http://fixture-proxy.invalid:1234", call.proxy)
		require.Equal(t, int64(42), call.accountID)
		require.Equal(t, 3, call.concurrency)
		require.NotNil(t, call.profile)
		require.True(t, HTTPUpstreamRedirectsDisabled(call.request.Context()))
		require.Equal(t, HTTPUpstreamProfileLongStream, HTTPUpstreamProfileFromContext(call.request.Context()))
		_, hasDeadline := call.request.Context().Deadline()
		require.True(t, hasDeadline)
	}
}

func TestPreparePerplexityImageAttachmentsRejectsAllInputsBeforeOutbound(t *testing.T) {
	imageData := perplexityAttachmentPNG(t)
	valid := perplexityAttachmentPart(perplexityAttachmentURL("image/png", imageData))
	wide := bytes.Clone(imageData)
	binary.BigEndian.PutUint32(wide[16:20], perplexity.MaxImageDimension+1)
	binary.BigEndian.PutUint32(wide[29:33], crc32.ChecksumIEEE(wide[12:29]))
	cases := []struct {
		name string
		body []byte
	}{
		{name: "malformed JSON", body: []byte(`{"messages":`)},
		{name: "malformed base64 after valid image", body: perplexityAttachmentBody(t, valid, perplexityAttachmentPart("data:image/png;base64,!!!!"))},
		{name: "MIME mismatch", body: perplexityAttachmentBody(t, valid, perplexityAttachmentPart(perplexityAttachmentURL("image/webp", imageData)))},
		{name: "remote URL", body: perplexityAttachmentBody(t, valid, perplexityAttachmentPart("https://example.com/image.png"))},
		{name: "private URL", body: perplexityAttachmentBody(t, valid, perplexityAttachmentPart("https://169.254.169.254/latest/meta-data"))},
		{name: "dimensions", body: perplexityAttachmentBody(t, valid, perplexityAttachmentPart(perplexityAttachmentURL("image/png", wide)))},
		{name: "file ID", body: perplexityAttachmentBody(t, valid, map[string]any{"type": "input_image", "file_id": "file-test"})},
		{name: "document", body: perplexityAttachmentBody(t, valid, map[string]any{"type": "input_file", "file_data": "anything"})},
		{name: "six images", body: perplexityAttachmentBody(t, valid, valid, valid, valid, valid, valid)},
		{name: "oversized image", body: perplexityAttachmentBody(t, valid, perplexityAttachmentPart("data:image/png;base64,"+strings.Repeat("A", 4*((perplexity.MaxImageAttachmentBytes+2)/3)+4)))},
		{name: "no user message", body: []byte(strings.ReplaceAll(string(perplexityAttachmentBody(t, valid)), `"role":"user"`, `"role":"assistant"`))},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &perplexityAttachmentUpstream{t: t, respond: func(int, *http.Request) (*http.Response, error) {
				t.Fatal("invalid input must not cause any outbound request")
				return nil, nil
			}}
			svc := &OpenAIGatewayService{httpUpstream: upstream}
			sanitized, urls, err := svc.preparePerplexityImageAttachments(t.Context(), perplexityAttachmentAccount(), tt.body)
			var badRequest *perplexityBadRequestError
			require.ErrorAs(t, err, &badRequest, "invalid input must map to an explicit 400")
			require.Nil(t, sanitized)
			require.Nil(t, urls)
			require.Empty(t, upstream.calls)
		})
	}
}

func TestPreparePerplexityImageAttachmentsAssignedProxyUnavailable(t *testing.T) {
	account := perplexityAttachmentAccount()
	account.Proxy = nil
	upstream := &perplexityAttachmentUpstream{t: t, respond: func(int, *http.Request) (*http.Response, error) {
		t.Fatal("unavailable assigned proxy must fail before every request")
		return nil, nil
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	_, _, err := svc.preparePerplexityImageAttachments(t.Context(), account, perplexityAttachmentBody(t, perplexityAttachmentPart(perplexityAttachmentURL("image/png", perplexityAttachmentPNG(t)))))
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.Empty(t, upstream.calls)
}

func TestPreparePerplexityImageAttachmentsStopsOnMetadataOrStorageFailure(t *testing.T) {
	bucket := "https://pplx-fixture.s3.amazonaws.com/"
	object := bucket + "user_uploads/image-1.png"
	validMetadata := perplexityAttachmentMetadata(t, bucket, object, map[string]any{"key": "user_uploads/image-1.png", "policy": "signed"})
	for _, tt := range []struct {
		name           string
		metadataStatus int
		metadataBody   string
		uploadStatus   int
		uploadErr      error
		wantCalls      int
	}{
		{name: "metadata redirect", metadataStatus: 302, metadataBody: "", wantCalls: 1},
		{name: "metadata auth failure", metadataStatus: 401, metadataBody: "", wantCalls: 1},
		{name: "invalid metadata", metadataStatus: 200, metadataBody: `{`, wantCalls: 1},
		{name: "evil S3", metadataStatus: 200, metadataBody: perplexityAttachmentMetadata(t, "https://evil.test/", object, map[string]any{"key": "user_uploads/image-1.png"}), wantCalls: 1},
		{name: "evil object", metadataStatus: 200, metadataBody: perplexityAttachmentMetadata(t, bucket, "https://127.0.0.1/private", map[string]any{"key": "user_uploads/image-1.png"}), wantCalls: 1},
		{name: "document upload_url unsupported", metadataStatus: 200, metadataBody: `{"upload_url":"https://pplx-fixture.s3.amazonaws.com/","file_uuid":"doc","processing":true}`, wantCalls: 1},
		{name: "metadata size", metadataStatus: 200, metadataBody: strings.Repeat("A", perplexity.MaxUploadMetadataBytes+1), wantCalls: 1},
		{name: "upload redirect", metadataStatus: 200, metadataBody: validMetadata, uploadStatus: 307, wantCalls: 2},
		{name: "upload pending is not completion", metadataStatus: 200, metadataBody: validMetadata, uploadStatus: 202, wantCalls: 2},
		{name: "upload failure", metadataStatus: 200, metadataBody: validMetadata, uploadStatus: 403, wantCalls: 2},
		{name: "upload transport failure", metadataStatus: 200, metadataBody: validMetadata, uploadErr: errors.New("fixture storage failure"), wantCalls: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &perplexityAttachmentUpstream{t: t}
			upstream.respond = func(index int, req *http.Request) (*http.Response, error) {
				require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
				require.Equal(t, "http://fixture-proxy.invalid:1234", upstream.calls[index].proxy)
				if index == 0 {
					response := perplexityAttachmentResponse(tt.metadataStatus, tt.metadataBody)
					response.Header.Set("Location", "https://127.0.0.1/private")
					return response, nil
				}
				require.Equal(t, 1, index, "must stop without asking or processing a document")
				require.Empty(t, req.Header.Get("Cookie"))
				require.Empty(t, req.Header.Get("Authorization"))
				if tt.uploadErr != nil {
					return nil, tt.uploadErr
				}
				response := perplexityAttachmentResponse(tt.uploadStatus, "")
				response.Header.Set("Location", "https://127.0.0.1/private")
				return response, nil
			}
			svc := &OpenAIGatewayService{httpUpstream: upstream}
			part := perplexityAttachmentPart(perplexityAttachmentURL("image/png", perplexityAttachmentPNG(t)))
			sanitized, urls, err := svc.preparePerplexityImageAttachments(t.Context(), perplexityAttachmentAccount(), perplexityAttachmentBody(t, part, part))
			require.Error(t, err)
			require.Nil(t, sanitized)
			require.Nil(t, urls, "a failed upload must never look like accepted attachments")
			require.Len(t, upstream.calls, tt.wantCalls)
		})
	}
}

func TestPreparePerplexityImageAttachmentsCancellationAndNoImages(t *testing.T) {
	upstream := &perplexityAttachmentUpstream{t: t, respond: func(int, *http.Request) (*http.Response, error) {
		t.Fatal("unexpected outbound request")
		return nil, nil
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	body := perplexityAttachmentBody(t, map[string]any{"type": "text", "text": "hello"})
	sanitized, urls, err := svc.preparePerplexityImageAttachments(t.Context(), perplexityAttachmentAccount(), body)
	require.NoError(t, err)
	require.Equal(t, body, sanitized)
	require.Nil(t, urls)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = svc.preparePerplexityImageAttachments(ctx, perplexityAttachmentAccount(), perplexityAttachmentBody(t, perplexityAttachmentPart(perplexityAttachmentURL("image/png", perplexityAttachmentPNG(t)))))
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, upstream.calls)
}
