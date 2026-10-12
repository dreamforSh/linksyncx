package service

import (
	"context"
	"math/rand/v2"
	"net/http"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// 批量设备画像操作。
const (
	DeviceProfileBatchActionRandomize = "randomize"
	DeviceProfileBatchActionClear     = "clear"

	// MaxDeviceProfileBatchAccounts 限制单次批量操作的账号数（逐账号写库）。
	MaxDeviceProfileBatchAccounts = 1000

	// 批量结果状态。
	DeviceProfileBatchStatusUpdated = "updated"
	DeviceProfileBatchStatusSkipped = "skipped"
	DeviceProfileBatchStatusFailed  = "failed"

	// 跳过原因（前端按此映射文案）。
	DeviceProfileSkipNotFound    = "not_found"
	DeviceProfileSkipShadow      = "shadow"
	DeviceProfileSkipUnsupported = "unsupported"

	// deviceProfileUniqueAttempts 是同一批次内为避开重复画像的最多生成次数。
	deviceProfileUniqueAttempts = 8
)

// DeviceProfileBatchInput 批量设备画像操作的输入。
type DeviceProfileBatchInput struct {
	AccountIDs []int64
	Action     string
}

// DeviceProfileBatchItem 单个账号的处理结果。
type DeviceProfileBatchItem struct {
	AccountID     int64          `json:"account_id"`
	Name          string         `json:"name,omitempty"`
	Platform      string         `json:"platform,omitempty"`
	Status        string         `json:"status"`
	Reason        string         `json:"reason,omitempty"`
	Profile       *DeviceProfile `json:"profile,omitempty"`
	IdentityReset bool           `json:"identity_reset,omitempty"`
}

// DeviceProfileBatchResult 批量设备画像操作的汇总结果。
type DeviceProfileBatchResult struct {
	Updated int                      `json:"updated"`
	Skipped int                      `json:"skipped"`
	Failed  int                      `json:"failed"`
	Results []DeviceProfileBatchItem `json:"results"`
}

// BatchUpdateDeviceProfiles 为一批账号随机分配各不相同的设备画像，或清除画像。
// 影子账号与不支持的账号类型跳过；每个账号单独做 JSONB 合并写入，不触碰其它凭据键。
func (s *adminServiceImpl) BatchUpdateDeviceProfiles(ctx context.Context, input *DeviceProfileBatchInput) (*DeviceProfileBatchResult, error) {
	if input == nil || len(input.AccountIDs) == 0 {
		return nil, infraerrors.New(http.StatusBadRequest, deviceProfileErrorCode, "account_ids is required")
	}
	if len(input.AccountIDs) > MaxDeviceProfileBatchAccounts {
		return nil, infraerrors.Newf(http.StatusBadRequest, deviceProfileErrorCode,
			"at most %d accounts per batch", MaxDeviceProfileBatchAccounts)
	}
	if input.Action != DeviceProfileBatchActionRandomize && input.Action != DeviceProfileBatchActionClear {
		return nil, infraerrors.Newf(http.StatusBadRequest, deviceProfileErrorCode, "unsupported action %q", input.Action)
	}

	accounts, err := s.accountRepo.GetByIDs(ctx, input.AccountIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*Account, len(accounts))
	for _, account := range accounts {
		if account != nil {
			byID[account.ID] = account
		}
	}

	result := &DeviceProfileBatchResult{Results: make([]DeviceProfileBatchItem, 0, len(input.AccountIDs))}
	seen := make(map[DeviceProfile]struct{}, len(input.AccountIDs))
	processed := make(map[int64]struct{}, len(input.AccountIDs))
	for _, id := range input.AccountIDs {
		if _, dup := processed[id]; dup {
			continue
		}
		processed[id] = struct{}{}

		item := DeviceProfileBatchItem{AccountID: id}
		account := byID[id]
		switch {
		case account == nil:
			item.Status, item.Reason = DeviceProfileBatchStatusSkipped, DeviceProfileSkipNotFound
		case account.IsShadow():
			item.Status, item.Reason = DeviceProfileBatchStatusSkipped, DeviceProfileSkipShadow
		case !IsDeviceProfileEligible(account.Platform, account.Type):
			item.Status, item.Reason = DeviceProfileBatchStatusSkipped, DeviceProfileSkipUnsupported
		}
		if account != nil {
			item.Name, item.Platform = account.Name, account.Platform
		}
		if item.Status != "" {
			result.Skipped++
			result.Results = append(result.Results, item)
			continue
		}

		var profile DeviceProfile
		if input.Action == DeviceProfileBatchActionRandomize {
			profile, err = generateDistinctDeviceProfile(account.Platform, seen)
			if err != nil {
				return nil, err
			}
		}
		updates := deviceProfileCredentialUpdates(account.Platform, profile)
		if _, err := s.accountRepo.BulkUpdate(ctx, []int64{id}, AccountBulkUpdate{Credentials: updates}); err != nil {
			item.Status, item.Reason = DeviceProfileBatchStatusFailed, err.Error()
			result.Failed++
			result.Results = append(result.Results, item)
			continue
		}
		if input.Action == DeviceProfileBatchActionRandomize {
			p := profile
			item.Profile = &p
		}
		item.Status = DeviceProfileBatchStatusUpdated
		result.Updated++
		result.Results = append(result.Results, item)
	}
	return result, nil
}

// NewDistinctDeviceProfileGenerator 返回逐个生成画像的函数；同一生成器产出的画像
// 尽量互不重复，供导入等无法预知账号数的批量创建流程使用。
func NewDistinctDeviceProfileGenerator(platform string) func() (DeviceProfile, error) {
	seen := map[DeviceProfile]struct{}{}
	return func() (DeviceProfile, error) {
		return generateDistinctDeviceProfile(platform, seen)
	}
}

// GenerateDistinctDeviceProfiles 一次生成 count 份尽量互不重复的画像，
// 供「添加账号」批量创建时逐个分配。
func GenerateDistinctDeviceProfiles(platform string, count int) ([]DeviceProfile, error) {
	if count < 1 || count > MaxDeviceProfileBatchAccounts {
		return nil, infraerrors.Newf(http.StatusBadRequest, deviceProfileErrorCode,
			"count must be between 1 and %d", MaxDeviceProfileBatchAccounts)
	}
	next := NewDistinctDeviceProfileGenerator(platform)
	profiles := make([]DeviceProfile, 0, count)
	for i := 0; i < count; i++ {
		profile, err := next()
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

// generateDistinctDeviceProfile 生成一份本批次内尚未出现过的画像：先按真实分布加权抽样，
// 连续撞重后从未用过的组合里均匀挑选；取值空间耗尽（Claude 只有少数 OS / 架构组合）时接受重复。
func generateDistinctDeviceProfile(platform string, seen map[DeviceProfile]struct{}) (DeviceProfile, error) {
	var profile DeviceProfile
	for attempt := 0; attempt < deviceProfileUniqueAttempts; attempt++ {
		generated, err := GenerateDeviceProfile(platform)
		if err != nil {
			return DeviceProfile{}, err
		}
		profile = generated
		if _, dup := seen[profile]; !dup {
			seen[profile] = struct{}{}
			return profile, nil
		}
	}
	var unused []DeviceProfile
	for _, candidate := range allDeviceProfiles(platform) {
		if _, dup := seen[candidate]; !dup {
			unused = append(unused, candidate)
		}
	}
	if len(unused) > 0 {
		profile = unused[rand.IntN(len(unused))]
	}
	seen[profile] = struct{}{}
	return profile, nil
}
