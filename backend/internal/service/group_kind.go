package service

import (
	"context"
	"fmt"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// 管理分组（kind=managed）与渠道分组并列：不挂渠道、强制专属，组账号独占于该分组，
// 由组管理员分配给组成员。以下错误与校验是它的统一收口。
var (
	ErrInvalidGroupKind     = infraerrors.BadRequest("INVALID_GROUP_KIND", "group kind must be channel or managed")
	ErrGroupKindImmutable   = infraerrors.BadRequest("GROUP_KIND_IMMUTABLE", "group kind cannot be changed after creation")
	ErrInvalidGroupCategory = infraerrors.BadRequest("INVALID_GROUP_CATEGORY", "managed group category must be enterprise or team")
	ErrGroupCategoryChannel = infraerrors.BadRequest("INVALID_GROUP_CATEGORY", "only managed groups can have a category")

	ErrManagedGroupAccountExclusive = infraerrors.BadRequest("MANAGED_GROUP_ACCOUNT_EXCLUSIVE", "accounts in a managed group cannot belong to any other group")
	ErrManagedGroupPlatformMismatch = infraerrors.BadRequest("MANAGED_GROUP_PLATFORM_MISMATCH", "account platform must match the managed group platform")
	ErrManagedGroupInChannel        = infraerrors.BadRequest("MANAGED_GROUP_NOT_ALLOWED_IN_CHANNEL", "managed groups cannot be linked to channels")
	ErrManagedGroupAsFallback       = infraerrors.BadRequest("MANAGED_GROUP_CONSTRAINT", "managed groups cannot be used as fallback groups")
	ErrManagedGroupCopyAccounts     = infraerrors.BadRequest("MANAGED_GROUP_CONSTRAINT", "accounts cannot be copied into or out of managed groups")
	ErrManagedGroupDuplicate        = infraerrors.BadRequest("MANAGED_GROUP_CONSTRAINT", "managed groups cannot be duplicated because their accounts are exclusive")

	ErrAccountGroupRequired = infraerrors.BadRequest("ACCOUNT_GROUP_REQUIRED", "accounts must be bound to at least one group; create or pick a group first")
)

// NormalizeGroupKind 把空值视为渠道分组（存量数据与未显式指定 kind 的请求）；
// 非法值原样（小写）返回，交给 ValidateGroupKind 拒绝。
func NormalizeGroupKind(kind string) string {
	normalized := strings.ToLower(strings.TrimSpace(kind))
	if normalized == "" {
		return GroupKindChannel
	}
	return normalized
}

func ValidateGroupKind(kind string) error {
	switch kind {
	case GroupKindChannel, GroupKindManaged:
		return nil
	default:
		return ErrInvalidGroupKind
	}
}

// NormalizeGroupCategory 仅做大小写与空白归一；管理分组未指定分类时默认为 team。
func NormalizeGroupCategory(kind, category string) string {
	normalized := strings.ToLower(strings.TrimSpace(category))
	if kind == GroupKindManaged && normalized == "" {
		return GroupCategoryTeam
	}
	return normalized
}

// ValidateGroupCategory 要求管理分组的分类只能是 enterprise / team，渠道分组不能带分类。
func ValidateGroupCategory(kind, category string) error {
	if kind != GroupKindManaged {
		if category != "" {
			return ErrGroupCategoryChannel
		}
		return nil
	}
	if category != GroupCategoryEnterprise && category != GroupCategoryTeam {
		return ErrInvalidGroupCategory
	}
	return nil
}

func (g *Group) IsManaged() bool {
	return g != nil && g.Kind == GroupKindManaged
}

// validateManagedGroupShape 校验管理分组不变式：强制专属、仅标准计费、单一真实平台、不配置兜底。
// 调用方应先把可强制的字段（IsExclusive / SubscriptionType）归一后再调用。
func validateManagedGroupShape(group *Group) error {
	if !group.IsManaged() {
		return nil
	}
	if group.Platform == PlatformComposite {
		return infraerrors.BadRequest("MANAGED_GROUP_CONSTRAINT", "managed groups cannot use the composite platform")
	}
	if !group.IsExclusive {
		return infraerrors.BadRequest("MANAGED_GROUP_CONSTRAINT", "managed groups must stay exclusive")
	}
	if group.SubscriptionType != SubscriptionTypeStandard {
		return infraerrors.BadRequest("MANAGED_GROUP_CONSTRAINT", "managed groups only support standard (balance) billing")
	}
	if group.FallbackGroupID != nil || group.FallbackGroupIDOnInvalidRequest != nil {
		return infraerrors.BadRequest("MANAGED_GROUP_CONSTRAINT", "managed groups cannot configure fallback groups")
	}
	return nil
}

// ensureCopyAccountsAllowed 拒绝涉及管理分组的「从其他分组复制账号」：目标是管理分组，
// 或任一源分组是管理分组，都会让组账号出现在多个分组中。
func (s *adminServiceImpl) ensureCopyAccountsAllowed(ctx context.Context, target *Group, sourceGroupIDs []int64) error {
	if len(sourceGroupIDs) == 0 {
		return nil
	}
	if target.IsManaged() {
		return ErrManagedGroupCopyAccounts
	}
	for _, sourceID := range sourceGroupIDs {
		source, err := s.groupRepo.GetByIDLite(ctx, sourceID)
		if err != nil {
			return fmt.Errorf("source group %d not found: %w", sourceID, err)
		}
		if source.IsManaged() {
			return ErrManagedGroupCopyAccounts
		}
	}
	return nil
}

// GroupIDsByPlatform 把多平台导入（JSON 导入、CRS 同步）的目标分组按平台分桶：
// 每个账号只绑定与其平台相同的目标分组。同一平台若包含管理分组，它必须是该平台唯一的
// 目标分组，否则会破坏组账号独占。
func GroupIDsByPlatform(groups []*Group) (map[string][]int64, error) {
	byPlatform := make(map[string][]int64, len(groups))
	managedPlatforms := make(map[string]bool)
	seen := make(map[int64]struct{}, len(groups))
	for _, group := range groups {
		if group == nil {
			continue
		}
		if _, ok := seen[group.ID]; ok {
			continue
		}
		seen[group.ID] = struct{}{}
		byPlatform[group.Platform] = append(byPlatform[group.Platform], group.ID)
		if group.IsManaged() {
			managedPlatforms[group.Platform] = true
		}
	}
	for platform := range managedPlatforms {
		if len(byPlatform[platform]) > 1 {
			return nil, ErrManagedGroupAccountExclusive
		}
	}
	return byPlatform, nil
}
