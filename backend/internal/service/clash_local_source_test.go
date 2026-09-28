//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClashFileProfileLifecycle(t *testing.T) {
	env := newClashTestEnv(t, nil)
	ctx := context.Background()
	content := clashSubscriptionYAML("HK 01", "US 01", "剩余流量 10GB")
	interval := 60
	fetchProxy := int64(7)

	summary, refresh, err := env.svc.CreateProfile(ctx, ClashProfileInput{
		Name:                   strPtr("本地文件"),
		SourceType:             strPtr(ClashProfileSourceFile),
		Content:                &content,
		SourceName:             strPtr(`C:\Users\me\机场.yaml`),
		RefreshIntervalMinutes: &interval,
		FetchProxyID:           &fetchProxy,
	})
	require.NoError(t, err)
	require.NotNil(t, refresh)
	require.Equal(t, ClashRefreshOK, refresh.Status, refresh.Error)
	require.Equal(t, 2, refresh.Inserted, "the info node is excluded by default")
	require.Equal(t, ClashProfileSourceFile, summary.SourceType)
	require.Equal(t, "机场.yaml", summary.SourceName, "only the base name is kept")
	require.EqualValues(t, len(content), summary.SourceSize)
	require.Zero(t, summary.RefreshIntervalMinutes, "an uploaded file is never refreshed on a schedule")
	require.Nil(t, summary.FetchProxyID)
	require.Empty(t, summary.URLMasked)
	require.Empty(t, summary.ContentEncrypted, "the file is not loaded with the profile")
	stored := env.repo.profiles[summary.ID]
	require.Equal(t, "enc:"+content, stored.ContentEncrypted, "the file is stored encrypted")
	require.Equal(t, clashContentFingerprint(content), stored.URLFingerprint)

	// The same file cannot be imported twice.
	_, _, err = env.svc.CreateProfile(ctx, ClashProfileInput{Name: strPtr("again"), SourceType: strPtr(ClashProfileSourceFile), Content: &content})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)

	// Refreshing re-parses the stored file.
	refresh, err = env.svc.RefreshProfile(ctx, summary.ID, false)
	require.NoError(t, err)
	require.Equal(t, ClashRefreshOK, refresh.Status, refresh.Error)
	require.Equal(t, 2, refresh.Updated)

	// A file profile takes neither a URL nor a new source.
	_, err = env.svc.UpdateProfile(ctx, summary.ID, ClashProfileInput{URL: strPtr("https://example.com/sub")})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
	_, err = env.svc.UpdateProfile(ctx, summary.ID, ClashProfileInput{SourceType: strPtr(ClashProfileSourceURL)})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
	// A broken upload leaves the stored file alone.
	_, err = env.svc.UpdateProfile(ctx, summary.ID, ClashProfileInput{Content: strPtr("hello world")})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
	require.Equal(t, "enc:"+content, env.repo.profiles[summary.ID].ContentEncrypted)

	// Replacing the file keeps the profile settings and applies on refresh.
	replacement := clashSubscriptionYAML("HK 01", "US 01", "JP 01")
	updated, err := env.svc.UpdateProfile(ctx, summary.ID, ClashProfileInput{
		Name:                   strPtr("本地文件 v2"),
		Content:                &replacement,
		SourceName:             strPtr("v2.yml"),
		RefreshIntervalMinutes: &interval,
	})
	require.NoError(t, err)
	require.Equal(t, "本地文件 v2", updated.Name)
	require.Equal(t, "v2.yml", updated.SourceName)
	require.EqualValues(t, len(replacement), updated.SourceSize)
	require.Zero(t, updated.RefreshIntervalMinutes)
	require.Equal(t, "enc:"+replacement, env.repo.profiles[summary.ID].ContentEncrypted)
	require.Equal(t, clashContentFingerprint(replacement), env.repo.profiles[summary.ID].URLFingerprint)
	refresh, err = env.svc.RefreshProfile(ctx, summary.ID, false)
	require.NoError(t, err)
	require.Equal(t, ClashRefreshOK, refresh.Status, refresh.Error)
	require.Equal(t, 1, refresh.Inserted)
	require.Equal(t, 2, refresh.Updated)

	// Settings-only updates keep the file.
	_, err = env.svc.UpdateProfile(ctx, summary.ID, ClashProfileInput{Notes: strPtr("note")})
	require.NoError(t, err)
	require.Equal(t, "enc:"+replacement, env.repo.profiles[summary.ID].ContentEncrypted)
	require.Equal(t, "v2.yml", env.repo.profiles[summary.ID].SourceName)

	// A profile without a stored file reports the refresh error.
	env.repo.profiles[summary.ID].ContentEncrypted = ""
	refresh, err = env.svc.RefreshProfile(ctx, summary.ID, false)
	require.NoError(t, err)
	require.Equal(t, ClashRefreshError, refresh.Status)
	require.Contains(t, refresh.Error, "upload the file or paste the links again")
}

func TestClashFileProfileValidation(t *testing.T) {
	env := newClashTestEnv(t, nil)
	ctx := context.Background()
	file := strPtr(ClashProfileSourceFile)
	content := clashSubscriptionYAML("HK 01")

	_, _, err := env.svc.CreateProfile(ctx, ClashProfileInput{Name: strPtr("a"), SourceType: file})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
	_, _, err = env.svc.CreateProfile(ctx, ClashProfileInput{Name: strPtr("a"), SourceType: file, Content: &content, URL: strPtr("https://example.com/sub")})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
	_, _, err = env.svc.CreateProfile(ctx, ClashProfileInput{Name: strPtr("a"), SourceType: strPtr("ftp"), Content: &content})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
	_, _, err = env.svc.CreateProfile(ctx, ClashProfileInput{Name: strPtr("a"), SourceType: file, Content: strPtr("proxies: []\n")})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)

	// Uploads obey the subscription size limit.
	env.cfg.ClashPool.Subscription.MaxBodyBytes = 32
	_, _, err = env.svc.CreateProfile(ctx, ClashProfileInput{Name: strPtr("a"), SourceType: file, Content: &content})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
	require.Contains(t, err.Error(), "exceeds")
	env.cfg.ClashPool.Subscription.MaxBodyBytes = 1 << 20

	// URL profiles do not accept a file.
	url := "https://sub.example.com/api?token=x"
	env.repo.mu.Lock()
	urlProfile := &ClashProfile{ID: env.repo.id(), Name: "url", SourceType: ClashProfileSourceURL, Enabled: false}
	env.repo.profiles[urlProfile.ID] = urlProfile
	env.repo.mu.Unlock()
	_, err = env.svc.UpdateProfile(ctx, urlProfile.ID, ClashProfileInput{Content: &content})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
	_, err = env.svc.UpdateProfile(ctx, urlProfile.ID, ClashProfileInput{SourceType: file, URL: &url})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)

	// Files are stored encrypted, so they need the fixed key too.
	env.cfg.Totp.EncryptionKeyConfigured = false
	_, _, err = env.svc.CreateProfile(ctx, ClashProfileInput{Name: strPtr("b"), SourceType: file, Content: &content})
	requireClashReason(t, err, ClashErrCodeEncryptionRequired)
}

func TestClashPreviewUploadedFile(t *testing.T) {
	env := newClashTestEnv(t, nil)
	ctx := context.Background()
	content := clashSubscriptionYAML("HK 01", "US 01", "剩余流量 10GB")
	result, err := env.svc.PreviewProfile(ctx, ClashProfileInput{SourceType: strPtr(ClashProfileSourceFile), Content: &content})
	require.NoError(t, err)
	require.Equal(t, "clash_yaml", result.Format)
	require.Equal(t, 3, result.NodeCount)
	require.Equal(t, 2, result.Usable)
	require.Nil(t, result.UserInfo)

	_, err = env.svc.PreviewProfile(ctx, ClashProfileInput{SourceType: strPtr(ClashProfileSourceFile), Content: strPtr("not yaml: [")})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
	_, err = env.svc.PreviewProfile(ctx, ClashProfileInput{SourceType: strPtr(ClashProfileSourceFile)})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
}

func TestClashScheduledRefreshSkipsLocalProfiles(t *testing.T) {
	env := newClashTestEnv(t, nil)
	ctx := context.Background()
	fileContent := clashSubscriptionYAML("HK 01")
	ssLink, _ := clashTestShareLinks()
	file, _, err := env.svc.CreateProfile(ctx, ClashProfileInput{Name: strPtr("file"), SourceType: strPtr(ClashProfileSourceFile), Content: &fileContent})
	require.NoError(t, err)
	links, _, err := env.svc.CreateProfile(ctx, ClashProfileInput{Name: strPtr("links"), SourceType: strPtr(ClashProfileSourceLinks), Content: &ssLink})
	require.NoError(t, err)
	// Even with an interval (e.g. set by hand in the database) nothing is fetched.
	for _, id := range []int64{file.ID, links.ID} {
		env.repo.profiles[id].RefreshIntervalMinutes = 5
		env.repo.profiles[id].LastRefreshAt = nil
	}
	before := len(env.repo.refreshes)
	manager := NewClashManager(env.svc, env.runtime, nil, nil, nil, env.cfg)
	manager.refreshDueProfiles(ctx)
	require.Len(t, env.repo.refreshes, before)
}

// clashTestShareLinks returns share links with made-up credentials: an ss link
// in SIP002 form with an obfs plugin, as providers hand them out, and a trojan
// link.
func clashTestShareLinks() (ssLink, trojanLink string) {
	userinfo := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:test-password"))
	ssLink = "ss://" + userinfo + "@us.example.com:13277?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dcdn.example.com" +
		"#%F0%9F%87%BA%F0%9F%87%B8%20%E7%BE%8E%E5%9B%BD-%E6%B4%9B%E6%9D%89%E7%9F%B6%2004"
	trojanLink = "trojan://secret@jp.example.com:443?sni=jp.example.com#JP%2001"
	return ssLink, trojanLink
}

func TestClashLinksProfileLifecycle(t *testing.T) {
	env := newClashTestEnv(t, nil)
	ctx := context.Background()
	ssLink, trojanLink := clashTestShareLinks()
	links := strPtr(ClashProfileSourceLinks)

	// A single node link is enough; whitespace around it does not matter.
	pasted := "  " + ssLink + "  \r\n\r\n"
	summary, refresh, err := env.svc.CreateProfile(ctx, ClashProfileInput{
		Name: strPtr("美国 04"), SourceType: links, Content: &pasted, SourceName: strPtr("ignored.txt"),
	})
	require.NoError(t, err)
	require.NotNil(t, refresh)
	require.Equal(t, ClashRefreshOK, refresh.Status, refresh.Error)
	require.Equal(t, 1, refresh.Inserted)
	require.Equal(t, ClashProfileSourceLinks, summary.SourceType)
	require.Empty(t, summary.SourceName, "pasted links have no file name")
	require.Zero(t, summary.RefreshIntervalMinutes)
	require.Equal(t, "enc:"+ssLink, env.repo.profiles[summary.ID].ContentEncrypted, "links are stored trimmed and encrypted")
	require.EqualValues(t, len(ssLink), summary.SourceSize)

	nodes, err := env.repo.ListNodesByProfile(ctx, summary.ID)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	node := nodes[0]
	require.Equal(t, "🇺🇸 美国-洛杉矶 04", node.Name)
	require.Equal(t, "ss", node.Type)
	require.Equal(t, "us.example.com", node.Server)
	require.Equal(t, 13277, node.ServerPort)
	require.Equal(t, "aes-128-gcm", node.Config["cipher"])
	require.Equal(t, "test-password", node.Config["password"])
	require.Equal(t, "obfs", node.Config["plugin"], "the SIP002 obfs-local plugin becomes mihomo's obfs plugin")
	require.Equal(t, map[string]any{"mode": "http", "host": "cdn.example.com"}, node.Config["plugin-opts"])

	// The same link pasted again is a duplicate.
	again := ssLink + "\n"
	_, _, err = env.svc.CreateProfile(ctx, ClashProfileInput{Name: strPtr("dup"), SourceType: links, Content: &again})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
	require.Contains(t, err.Error(), "node links are already imported")

	// Replacing the links (here: adding a node) applies on refresh.
	both := ssLink + "\n" + trojanLink
	updated, err := env.svc.UpdateProfile(ctx, summary.ID, ClashProfileInput{Content: &both})
	require.NoError(t, err)
	require.Empty(t, updated.SourceName)
	require.Equal(t, "enc:"+both, env.repo.profiles[summary.ID].ContentEncrypted)
	refresh, err = env.svc.RefreshProfile(ctx, summary.ID, false)
	require.NoError(t, err)
	require.Equal(t, ClashRefreshOK, refresh.Status, refresh.Error)
	require.Equal(t, 1, refresh.Inserted)
	require.Equal(t, 1, refresh.Updated)

	// The source is fixed and links profiles take no url.
	_, err = env.svc.UpdateProfile(ctx, summary.ID, ClashProfileInput{SourceType: strPtr(ClashProfileSourceFile)})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
	_, err = env.svc.UpdateProfile(ctx, summary.ID, ClashProfileInput{URL: strPtr("https://example.com/sub")})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
	// Text without a usable link is rejected before anything is stored.
	_, err = env.svc.UpdateProfile(ctx, summary.ID, ClashProfileInput{Content: strPtr("hello\nworld")})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
	require.Contains(t, err.Error(), "no usable node link")
	require.Equal(t, "enc:"+both, env.repo.profiles[summary.ID].ContentEncrypted)
}

func TestClashPreviewPastedLinks(t *testing.T) {
	env := newClashTestEnv(t, nil)
	ctx := context.Background()
	ssLink, trojanLink := clashTestShareLinks()
	links := strPtr(ClashProfileSourceLinks)
	content := ssLink + "\r\n\r\n" + trojanLink + "\n"
	result, err := env.svc.PreviewProfile(ctx, ClashProfileInput{SourceType: links, Content: &content})
	require.NoError(t, err)
	require.Equal(t, "uri_list", result.Format)
	require.Equal(t, 2, result.NodeCount)
	require.Equal(t, 2, result.Usable)
	require.Equal(t, "🇺🇸 美国-洛杉矶 04", result.Nodes[0].Name)
	require.Equal(t, "JP 01", result.Nodes[1].Name)

	_, err = env.svc.PreviewProfile(ctx, ClashProfileInput{SourceType: links, Content: strPtr("  \n ")})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
	_, err = env.svc.PreviewProfile(ctx, ClashProfileInput{SourceType: strPtr("smb"), Content: &content})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
}

func TestLocalClashContent(t *testing.T) {
	require.Equal(t, "a\nb", localClashContent(ClashProfileSourceLinks, " a \r\n\n\tb\n"))
	require.Equal(t, " a \r\n", localClashContent(ClashProfileSourceFile, " a \r\n"), "files are stored verbatim")
	require.True(t, isLocalClashSource(ClashProfileSourceFile))
	require.True(t, isLocalClashSource(ClashProfileSourceLinks))
	require.False(t, isLocalClashSource(ClashProfileSourceURL))
	require.False(t, isLocalClashSource(""))
}

func TestCleanClashFileName(t *testing.T) {
	require.Equal(t, "sub.yaml", cleanClashFileName(" /tmp/x/sub.yaml "))
	require.Equal(t, "sub.yaml", cleanClashFileName(`C:\fakepath\sub.yaml`))
	require.Equal(t, "", cleanClashFileName(""))
	require.Equal(t, "", cleanClashFileName("/"))
	require.Equal(t, clashFileNameLimit, len([]rune(cleanClashFileName(strings.Repeat("节", 300)))))
	require.NotEqual(t, clashContentFingerprint("a"), clashURLFingerprint("a"), "file digests never collide with URL digests")
}
