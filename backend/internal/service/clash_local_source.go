package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/clashsub"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// 本地来源的 Clash 订阅：上传的配置文件（file）或粘贴的节点分享链接（links，如 ss:// vmess:// trojan://）。
// 内容（含节点密码）与订阅链接一样加密存储；没有地址可拉取，不参与定时刷新，“刷新”即按当前的
// 包含/排除规则重新解析已保存的内容，更换内容需要重新上传文件或重新粘贴链接。

const clashFileNameLimit = 255

// normalizeClashSourceType maps the requested source ("" means url).
func normalizeClashSourceType(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", ClashProfileSourceURL:
		return ClashProfileSourceURL, nil
	case ClashProfileSourceFile:
		return ClashProfileSourceFile, nil
	case ClashProfileSourceLinks:
		return ClashProfileSourceLinks, nil
	default:
		return "", infraerrors.BadRequest(ClashErrCodeProfileInvalid, "source_type must be url, file or links")
	}
}

// isLocalClashSource reports whether a source keeps its content in the profile
// instead of fetching it.
func isLocalClashSource(sourceType string) bool {
	return sourceType == ClashProfileSourceFile || sourceType == ClashProfileSourceLinks
}

// clashContentFingerprint 用内容摘要占用 url_fingerprint：同样的内容（文件或节点链接）不能被重复导入，
// 前缀把它和订阅链接的摘要区分开。
func clashContentFingerprint(content string) string {
	sum := sha256.Sum256([]byte("clash-file\x00" + content))
	return hex.EncodeToString(sum[:])
}

// localClashContent is the content as stored. Pasted links are trimmed line by
// line without blank lines, so the same links pasted again (or with Windows
// line endings) share one digest; files are kept verbatim.
func localClashContent(sourceType, content string) string {
	if sourceType != ClashProfileSourceLinks {
		return content
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// cleanClashFileName keeps only the base name of an uploaded file, trimmed to
// the column size.
func cleanClashFileName(name string) string {
	name = path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if name == "." || name == "/" {
		return ""
	}
	if utf8.RuneCountInString(name) > clashFileNameLimit {
		runes := []rune(name)
		name = string(runes[:clashFileNameLimit])
	}
	return name
}

// parseClashContent checks local content with the same limits as a fetched
// subscription (size and node count) and parses it.
func (s *ClashService) parseClashContent(sourceType, content string) (*clashsub.Result, error) {
	links := sourceType == ClashProfileSourceLinks
	if strings.TrimSpace(content) == "" {
		if links {
			return nil, infraerrors.BadRequest(ClashErrCodeProfileInvalid, "paste at least one node link")
		}
		return nil, infraerrors.BadRequest(ClashErrCodeProfileInvalid, "the uploaded file is empty")
	}
	if limit := s.fetchPolicy().MaxBodyBytes; limit > 0 && int64(len(content)) > limit {
		if links {
			return nil, infraerrors.BadRequest(ClashErrCodeProfileInvalid, fmt.Sprintf("the pasted links exceed %d bytes", limit))
		}
		return nil, infraerrors.BadRequest(ClashErrCodeProfileInvalid, fmt.Sprintf("the uploaded file exceeds %d bytes", limit))
	}
	parsed, err := clashsub.Parse([]byte(content), clashsub.Options{MaxNodes: s.maxNodesPerProfile()})
	if err != nil {
		if links {
			return nil, infraerrors.BadRequest(ClashErrCodeProfileInvalid, "no usable node link: "+err.Error())
		}
		return nil, infraerrors.BadRequest(ClashErrCodeProfileInvalid, "the uploaded file is not a usable Clash configuration: "+err.Error())
	}
	return parsed, nil
}

// setProfileContent validates, encrypts and attaches local content to a file
// or links profile. Content that cannot be parsed is rejected up front: unlike
// a subscription URL there is nothing to retry later.
func (s *ClashService) setProfileContent(ctx context.Context, profile *ClashProfile, content, fileName string) error {
	content = localClashContent(profile.SourceType, content)
	if _, err := s.parseClashContent(profile.SourceType, content); err != nil {
		return err
	}
	fingerprint := clashContentFingerprint(content)
	if exists, err := s.repo.ExistsProfileURL(ctx, fingerprint, profile.ID); err != nil {
		return err
	} else if exists {
		if profile.SourceType == ClashProfileSourceLinks {
			return infraerrors.Conflict(ClashErrCodeProfileInvalid, "these node links are already imported")
		}
		return infraerrors.Conflict(ClashErrCodeProfileInvalid, "this file is already imported")
	}
	encrypted, err := s.encryptor.Encrypt(content)
	if err != nil {
		return fmt.Errorf("encrypt subscription content: %w", err)
	}
	profile.ContentEncrypted = encrypted
	profile.URLFingerprint = fingerprint
	profile.SourceName = ""
	if profile.SourceType == ClashProfileSourceFile {
		profile.SourceName = cleanClashFileName(fileName)
	}
	profile.SourceSize = int64(len(content))
	return nil
}

// loadProfileContent decrypts and parses the stored content of a local profile.
func (s *ClashService) loadProfileContent(ctx context.Context, profile *ClashProfile) (*clashsub.Result, error) {
	encrypted, err := s.repo.GetProfileContent(ctx, profile.ID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(encrypted) == "" {
		return nil, errors.New("nothing is stored for this subscription; upload the file or paste the links again")
	}
	content, err := s.encryptor.Decrypt(encrypted)
	if err != nil {
		return nil, errors.New("stored subscription content cannot be decrypted; upload the file or paste the links again (encryption key changed?)")
	}
	return clashsub.Parse([]byte(content), clashsub.Options{MaxNodes: s.maxNodesPerProfile()})
}
