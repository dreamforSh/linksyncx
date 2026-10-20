//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// tlsProfileHandlerRepoStub 是内存版模板仓储，记录写入次数，用来确认被拒的模板没有落库。
type tlsProfileHandlerRepoStub struct {
	profiles map[int64]*model.TLSFingerprintProfile
	writes   int
}

func (r *tlsProfileHandlerRepoStub) List(context.Context) ([]*model.TLSFingerprintProfile, error) {
	out := make([]*model.TLSFingerprintProfile, 0, len(r.profiles))
	for _, p := range r.profiles {
		out = append(out, p)
	}
	return out, nil
}

func (r *tlsProfileHandlerRepoStub) GetByID(_ context.Context, id int64) (*model.TLSFingerprintProfile, error) {
	return r.profiles[id], nil
}

func (r *tlsProfileHandlerRepoStub) Create(_ context.Context, p *model.TLSFingerprintProfile) (*model.TLSFingerprintProfile, error) {
	r.writes++
	stored := *p
	stored.ID = int64(len(r.profiles) + 100)
	r.profiles[stored.ID] = &stored
	return &stored, nil
}

func (r *tlsProfileHandlerRepoStub) Update(_ context.Context, p *model.TLSFingerprintProfile) (*model.TLSFingerprintProfile, error) {
	r.writes++
	stored := *p
	r.profiles[stored.ID] = &stored
	return &stored, nil
}

func (r *tlsProfileHandlerRepoStub) Delete(_ context.Context, id int64) error {
	delete(r.profiles, id)
	return nil
}

func doTLSProfileHandlerRequest(t *testing.T, router *gin.Engine, method, path, body string) (int, tlsProfileHandlerResponse) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var resp tlsProfileHandlerResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	return rec.Code, resp
}

type tlsProfileHandlerResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// 不自洽的模板（自定义 curves 却没填 key_share_groups，key_share 继承内置默认的 4588）保存时必须得到
// 400 和指明 key_share_groups 的原因，而不是存进库后让绑定它的账号每个请求都在建池前失败；
// 部分更新时也按合并后的完整模板校验。前端直接展示这条 message。
func TestTLSFingerprintProfileHandlerRejectsInconsistentKeyShares(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &tlsProfileHandlerRepoStub{profiles: map[int64]*model.TLSFingerprintProfile{
		7: {ID: 7, Name: "existing", Curves: []uint16{29}, KeyShareGroups: []uint16{29}},
	}}
	handler := NewTLSFingerprintProfileHandler(service.NewTLSFingerprintProfileService(repo, nil))
	router := gin.New()
	router.POST("/admin/tls-fingerprint-profiles", handler.Create)
	router.PUT("/admin/tls-fingerprint-profiles/:id", handler.Update)

	status, resp := doTLSProfileHandlerRequest(t, router, http.MethodPost, "/admin/tls-fingerprint-profiles",
		`{"name":"node-like","curves":[29,23,24]}`)
	require.Equal(t, http.StatusBadRequest, status)
	require.Contains(t, resp.Message, "key_share_groups")
	require.Contains(t, resp.Message, "4588")

	// 只改 curves：合并后的 key_share_groups 仍是 [29]，而 29 已不在新 curves 里。
	status, resp = doTLSProfileHandlerRequest(t, router, http.MethodPut, "/admin/tls-fingerprint-profiles/7",
		`{"curves":[23,24]}`)
	require.Equal(t, http.StatusBadRequest, status)
	require.Contains(t, resp.Message, "key_share_groups")
	require.Zero(t, repo.writes, "rejected templates must not be stored")
	require.Equal(t, []uint16{29}, repo.profiles[7].Curves)

	status, _ = doTLSProfileHandlerRequest(t, router, http.MethodPost, "/admin/tls-fingerprint-profiles",
		`{"name":"node-like","curves":[29,23,24],"key_share_groups":[29]}`)
	require.Equal(t, http.StatusOK, status)
	status, _ = doTLSProfileHandlerRequest(t, router, http.MethodPut, "/admin/tls-fingerprint-profiles/7",
		`{"curves":[29,23,24]}`)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, 2, repo.writes)
}
