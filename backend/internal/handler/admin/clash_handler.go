package admin

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ClashHandler serves the Clash proxy pool admin API.
type ClashHandler struct {
	svc      *service.ClashService
	manager  *service.ClashManager
	settings *service.SettingService
}

// NewClashHandler creates the Clash pool handler.
func NewClashHandler(svc *service.ClashService, manager *service.ClashManager, settings *service.SettingService) *ClashHandler {
	return &ClashHandler{svc: svc, manager: manager, settings: settings}
}

type clashProfileRequest struct {
	Name                   *string `json:"name" binding:"omitempty,max=100"`
	URL                    *string `json:"url" binding:"omitempty,max=4096"`
	UserAgent              *string `json:"user_agent" binding:"omitempty,max=200"`
	Enabled                *bool   `json:"enabled"`
	RefreshIntervalMinutes *int    `json:"refresh_interval_minutes" binding:"omitempty,min=0,max=10080"`
	IncludePattern         *string `json:"include_pattern" binding:"omitempty,max=1000"`
	ExcludePattern         *string `json:"exclude_pattern" binding:"omitempty,max=1000"`
	// FetchProxyID selects a manual proxy for downloading; 0 clears it.
	FetchProxyID *int64  `json:"fetch_proxy_id"`
	Notes        *string `json:"notes" binding:"omitempty,max=2000"`
}

func (r *clashProfileRequest) toInput() service.ClashProfileInput {
	in := service.ClashProfileInput{
		Name:                   r.Name,
		URL:                    r.URL,
		UserAgent:              r.UserAgent,
		Enabled:                r.Enabled,
		RefreshIntervalMinutes: r.RefreshIntervalMinutes,
		IncludePattern:         r.IncludePattern,
		ExcludePattern:         r.ExcludePattern,
		Notes:                  r.Notes,
	}
	if r.FetchProxyID != nil {
		if *r.FetchProxyID <= 0 {
			in.ClearFetchProxy = true
		} else {
			in.FetchProxyID = r.FetchProxyID
		}
	}
	return in
}

type clashNodeSelectionRequest struct {
	NodeIDs   []int64 `json:"node_ids"`
	ProfileID *int64  `json:"profile_id"`
}

func parseClashID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid id")
		return 0, false
	}
	return id, true
}

// ListProfiles GET /admin/clash/profiles
func (h *ClashHandler) ListProfiles(c *gin.Context) {
	profiles, err := h.svc.ListProfiles(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]*dto.ClashProfile, 0, len(profiles))
	for i := range profiles {
		out = append(out, dto.ClashProfileFromService(&profiles[i]))
	}
	response.Success(c, out)
}

// GetProfile GET /admin/clash/profiles/:id
func (h *ClashHandler) GetProfile(c *gin.Context) {
	id, ok := parseClashID(c)
	if !ok {
		return
	}
	profile, err := h.svc.GetProfile(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.ClashProfileFromService(profile))
}

// CreateProfile POST /admin/clash/profiles
func (h *ClashHandler) CreateProfile(c *gin.Context) {
	var req clashProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	profile, refresh, err := h.svc.CreateProfile(c.Request.Context(), req.toInput())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"profile": dto.ClashProfileFromService(profile), "refresh": refresh})
}

// UpdateProfile PUT /admin/clash/profiles/:id
func (h *ClashHandler) UpdateProfile(c *gin.Context) {
	id, ok := parseClashID(c)
	if !ok {
		return
	}
	var req clashProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	profile, err := h.svc.UpdateProfile(c.Request.Context(), id, req.toInput())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.ClashProfileFromService(profile))
}

// DeleteProfile DELETE /admin/clash/profiles/:id?force=true
func (h *ClashHandler) DeleteProfile(c *gin.Context) {
	id, ok := parseClashID(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteProfile(c.Request.Context(), id, c.Query("force") == "true"); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// RefreshProfile POST /admin/clash/profiles/:id/refresh?force=true
func (h *ClashHandler) RefreshProfile(c *gin.Context) {
	id, ok := parseClashID(c)
	if !ok {
		return
	}
	result, err := h.svc.RefreshProfile(c.Request.Context(), id, c.Query("force") == "true")
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// PreviewProfile POST /admin/clash/profiles/preview
func (h *ClashHandler) PreviewProfile(c *gin.Context) {
	var req clashProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.svc.PreviewProfile(c.Request.Context(), req.toInput())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// parseClashNodeFilter reads the node listing filters shared by ListNodes and
// ListNodeIDs; it answers 400 itself when they are invalid.
func parseClashNodeFilter(c *gin.Context) (service.ClashNodeFilter, bool) {
	filter := service.ClashNodeFilter{
		Status: strings.TrimSpace(c.Query("status")),
		Health: strings.TrimSpace(c.Query("health")),
		Search: strings.TrimSpace(c.Query("search")),
		Sort:   service.NormalizeClashNodeSort(strings.TrimSpace(c.Query("sort"))),
	}
	if len(filter.Search) > 100 {
		filter.Search = filter.Search[:100]
	}
	if raw := strings.TrimSpace(c.Query("profile_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid profile_id")
			return filter, false
		}
		filter.ProfileID = &id
	}
	switch c.Query("bound") {
	case "true":
		bound := true
		filter.Bound = &bound
	case "false":
		bound := false
		filter.Bound = &bound
	}
	return filter, true
}

// ListNodes GET /admin/clash/nodes
func (h *ClashHandler) ListNodes(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	filter, ok := parseClashNodeFilter(c)
	if !ok {
		return
	}
	nodes, result, err := h.svc.ListNodes(c.Request.Context(), filter, pagination.PaginationParams{Page: page, PageSize: pageSize})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]*dto.ClashNode, 0, len(nodes))
	for i := range nodes {
		out = append(out, dto.ClashNodeFromService(&nodes[i]))
	}
	response.Paginated(c, out, result.Total, page, pageSize)
}

// ListNodeIDs GET /admin/clash/nodes/ids?live=true — every node matching the
// listing filters; live=true keeps only nodes that tests can reach.
func (h *ClashHandler) ListNodeIDs(c *gin.Context) {
	filter, ok := parseClashNodeFilter(c)
	if !ok {
		return
	}
	filter.LiveOnly = c.Query("live") == "true"
	ids, err := h.svc.ListNodeIDs(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ids": ids})
}

// EnableNode POST /admin/clash/nodes/:id/enable
func (h *ClashHandler) EnableNode(c *gin.Context) { h.setNodeEnabled(c, true) }

// DisableNode POST /admin/clash/nodes/:id/disable
func (h *ClashHandler) DisableNode(c *gin.Context) { h.setNodeEnabled(c, false) }

func (h *ClashHandler) setNodeEnabled(c *gin.Context, enabled bool) {
	id, ok := parseClashID(c)
	if !ok {
		return
	}
	if err := h.svc.SetNodeEnabled(c.Request.Context(), id, enabled); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"enabled": enabled})
}

// AcceptNodeExit POST /admin/clash/nodes/:id/accept-exit
func (h *ClashHandler) AcceptNodeExit(c *gin.Context) {
	id, ok := parseClashID(c)
	if !ok {
		return
	}
	if err := h.svc.AcceptNodeExit(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"accepted": true})
}

// TestNodesLatency POST /admin/clash/nodes/test-latency
func (h *ClashHandler) TestNodesLatency(c *gin.Context) {
	var req clashNodeSelectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	results, err := h.svc.TestNodesLatency(c.Request.Context(), req.NodeIDs, req.ProfileID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, results)
}

// ProbeNodesExit POST /admin/clash/nodes/probe-exit
func (h *ClashHandler) ProbeNodesExit(c *gin.Context) {
	var req clashNodeSelectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	results, err := h.svc.ProbeNodesExit(c.Request.Context(), req.NodeIDs, req.ProfileID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, results)
}

// ListExits GET /admin/clash/exits
func (h *ClashHandler) ListExits(c *gin.Context) {
	list, err := h.svc.ListExits(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.ClashExitListFromService(list))
}

// RuntimeStatus GET /admin/clash/runtime
func (h *ClashHandler) RuntimeStatus(c *gin.Context) {
	response.Success(c, dto.ClashRuntimeStatusFromService(h.manager.Status(c.Request.Context())))
}

// ResyncRuntime POST /admin/clash/runtime/resync
func (h *ClashHandler) ResyncRuntime(c *gin.Context) {
	if err := h.manager.Resync(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.ClashRuntimeStatusFromService(h.manager.Status(c.Request.Context())))
}

// GetSettings GET /admin/clash/settings
func (h *ClashHandler) GetSettings(c *gin.Context) {
	settings, err := h.settings.GetClashPoolSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

// UpdateSettings PUT /admin/clash/settings
func (h *ClashHandler) UpdateSettings(c *gin.Context) {
	var req service.ClashPoolSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.settings.SetClashPoolSettings(c.Request.Context(), &req); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := h.svc.ReconcilePauses(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, req)
}
