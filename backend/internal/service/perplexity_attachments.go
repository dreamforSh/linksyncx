package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/tidwall/gjson"
)

// preparePerplexityImageAttachments validates every image before any outbound
// request, then authorizes and uploads the bounded inline images. The caller must
// pass the returned URLs to RequestOptions.Internal.Attachments and must not ask on
// error. This helper never downloads a client-supplied URL or processes documents.
func (s *OpenAIGatewayService) preparePerplexityImageAttachments(ctx context.Context, account *Account, chatBody []byte) ([]byte, []string, error) {
	sanitized, images, err := perplexity.PrepareImageAttachments(chatBody)
	if err != nil {
		return nil, nil, &perplexityBadRequestError{msg: err.Error()}
	}
	if len(images) == 0 {
		return sanitized, nil, nil
	}
	// Folding is also validation: a malformed conversation must not upload images
	// only to fail later because it has no usable user query.
	if _, err := perplexity.FoldMessagesToQuery(gjson.GetBytes(sanitized, "messages")); err != nil {
		return nil, nil, &perplexityBadRequestError{msg: err.Error()}
	}
	if s == nil || s.httpUpstream == nil || account == nil {
		return nil, nil, fmt.Errorf("perplexity image uploads are unavailable")
	}
	proxyURL, err := account.ProxyURLForOutbound()
	if err != nil {
		return nil, nil, err
	}
	creds := perplexityCredentialsFromAccount(account)
	if strings.TrimSpace(creds.SessionToken) == "" {
		return nil, nil, fmt.Errorf("perplexity image upload session token is missing")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	urls := make([]string, 0, len(images))
	for _, image := range images {
		metadata, err := s.authorizePerplexityImageUpload(ctx, account, perplexityCredentialsFromAccount(account), image)
		if err != nil {
			return nil, nil, err
		}
		if err := s.uploadPerplexityImage(ctx, account, proxyURL, image, metadata); err != nil {
			return nil, nil, err
		}
		urls = append(urls, metadata.ObjectURL)
	}
	return sanitized, urls, nil
}

func (s *OpenAIGatewayService) authorizePerplexityImageUpload(ctx context.Context, account *Account, creds perplexity.Credentials, image perplexity.ImageAttachment) (perplexity.ImageUploadMetadata, error) {
	body, err := perplexity.BuildImageUploadAuthorization(image)
	if err != nil {
		return perplexity.ImageUploadMetadata{}, fmt.Errorf("build Perplexity image authorization: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(WithHTTPUpstreamRedirectsDisabled(ctx), 30*time.Second)
	defer cancel()
	if err := requestCtx.Err(); err != nil {
		return perplexity.ImageUploadMetadata{}, err
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, creds.NormalizedBaseURL()+perplexity.ImageUploadAuthorizationPath, bytes.NewReader(body))
	if err != nil {
		return perplexity.ImageUploadMetadata{}, fmt.Errorf("build Perplexity image authorization request: %w", err)
	}
	creds.SetFetchStyleHeaders(req.Header, "application/json")
	// Only the authenticated Perplexity request may inherit account overrides.
	account.ApplyHeaderOverrides(req.Header)
	resp, err := sendPerplexityAuthenticated(s.httpUpstream, s.accountRepo, account, req, s.resolvePerplexityTLSProfile(account))
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return perplexity.ImageUploadMetadata{}, fmt.Errorf("perplexity image upload authorization failed: %w", err)
	}
	if resp == nil || resp.Body == nil {
		return perplexity.ImageUploadMetadata{}, fmt.Errorf("perplexity image upload authorization returned no response")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return perplexity.ImageUploadMetadata{}, fmt.Errorf("perplexity image upload authorization returned HTTP %d; redirects are not supported", resp.StatusCode)
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, perplexity.MaxUploadMetadataBytes+1))
	if err != nil {
		return perplexity.ImageUploadMetadata{}, fmt.Errorf("read Perplexity image upload authorization: %w", err)
	}
	metadata, err := perplexity.ParseImageUploadMetadata(responseBody, image)
	if err != nil {
		return perplexity.ImageUploadMetadata{}, err
	}
	return metadata, nil
}

func (s *OpenAIGatewayService) uploadPerplexityImage(ctx context.Context, account *Account, proxyURL string, image perplexity.ImageAttachment, metadata perplexity.ImageUploadMetadata) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	names := make([]string, 0, len(metadata.Fields))
	for name := range metadata.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := writer.WriteField(name, metadata.Fields[name]); err != nil {
			return fmt.Errorf("build S3 image form: %w", err)
		}
	}
	fileHeaders := make(textproto.MIMEHeader)
	fileHeaders.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, image.Filename))
	fileHeaders.Set("Content-Type", image.ContentType)
	filePart, err := writer.CreatePart(fileHeaders)
	if err != nil {
		return fmt.Errorf("build S3 image file part: %w", err)
	}
	if _, err := filePart.Write(image.Data); err != nil {
		return fmt.Errorf("write S3 image file part: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close S3 image form: %w", err)
	}

	// This destination came from an upstream response. Require public resolution
	// even when the deployment disables its global URL allowlist, and never follow
	// a redirect with either the signed form or the user's image bytes.
	requestCtx, cancel := context.WithTimeout(WithHTTPUpstreamPublicHostsOnly(WithHTTPUpstreamRedirectsDisabled(ctx)), 60*time.Second)
	defer cancel()
	if err := requestCtx.Err(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, metadata.BucketURL, &body)
	if err != nil {
		return fmt.Errorf("build S3 image upload request: %w", err)
	}
	// Start with fresh headers: neither subscription cookies, Authorization, nor
	// arbitrary account overrides may cross the Perplexity-to-storage boundary.
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "*/*")
	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.resolvePerplexityTLSProfile(account))
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return fmt.Errorf("S3 image upload failed: %w", err)
	}
	if resp == nil || resp.Body == nil {
		return fmt.Errorf("S3 image upload returned no response")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("S3 image upload returned HTTP %d; no attachment was accepted", resp.StatusCode)
	}
	// Only the reference's 200/201/204 storage acknowledgments complete an upload;
	// a 202 pending response is not an uploaded image. This image branch needs no
	// document attachment_processing polling.
	return nil
}
