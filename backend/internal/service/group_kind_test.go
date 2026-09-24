package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeAndValidateGroupKind(t *testing.T) {
	require.Equal(t, GroupKindChannel, NormalizeGroupKind(""))
	require.Equal(t, GroupKindManaged, NormalizeGroupKind(" Managed "))
	require.NoError(t, ValidateGroupKind(GroupKindChannel))
	require.NoError(t, ValidateGroupKind(GroupKindManaged))
	require.ErrorIs(t, ValidateGroupKind("org"), ErrInvalidGroupKind)
}

func TestNormalizeAndValidateGroupCategory(t *testing.T) {
	require.Equal(t, GroupCategoryTeam, NormalizeGroupCategory(GroupKindManaged, ""))
	require.Equal(t, GroupCategoryEnterprise, NormalizeGroupCategory(GroupKindManaged, " Enterprise "))
	require.Empty(t, NormalizeGroupCategory(GroupKindChannel, ""))

	require.NoError(t, ValidateGroupCategory(GroupKindManaged, GroupCategoryEnterprise))
	require.NoError(t, ValidateGroupCategory(GroupKindManaged, GroupCategoryTeam))
	require.ErrorIs(t, ValidateGroupCategory(GroupKindManaged, "department"), ErrInvalidGroupCategory)
	require.ErrorIs(t, ValidateGroupCategory(GroupKindChannel, GroupCategoryTeam), ErrGroupCategoryChannel)
	require.NoError(t, ValidateGroupCategory(GroupKindChannel, ""))
}

func TestValidateManagedGroupShape(t *testing.T) {
	valid := func() *Group {
		return &Group{Kind: GroupKindManaged, Platform: PlatformAnthropic, IsExclusive: true, SubscriptionType: SubscriptionTypeStandard}
	}
	require.NoError(t, validateManagedGroupShape(valid()))
	// 渠道分组不受管理分组约束
	require.NoError(t, validateManagedGroupShape(&Group{Kind: GroupKindChannel, Platform: PlatformComposite, SubscriptionType: SubscriptionTypeSubscription}))

	fallbackID := int64(3)
	cases := map[string]func(*Group){
		"composite platform":       func(g *Group) { g.Platform = PlatformComposite },
		"not exclusive":            func(g *Group) { g.IsExclusive = false },
		"subscription billing":     func(g *Group) { g.SubscriptionType = SubscriptionTypeSubscription },
		"fallback group":           func(g *Group) { g.FallbackGroupID = &fallbackID },
		"invalid request fallback": func(g *Group) { g.FallbackGroupIDOnInvalidRequest = &fallbackID },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			group := valid()
			mutate(group)
			require.Error(t, validateManagedGroupShape(group))
		})
	}
}

func TestGroupIDsByPlatformBucketsTargetsAndGuardsManagedExclusivity(t *testing.T) {
	byPlatform, err := GroupIDsByPlatform([]*Group{
		{ID: 1, Platform: PlatformAnthropic},
		{ID: 2, Platform: PlatformAnthropic},
		{ID: 3, Platform: PlatformOpenAI, Kind: GroupKindManaged},
		{ID: 1, Platform: PlatformAnthropic},
		nil,
	})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, byPlatform[PlatformAnthropic])
	require.Equal(t, []int64{3}, byPlatform[PlatformOpenAI])
	require.Empty(t, byPlatform[PlatformGemini])

	_, err = GroupIDsByPlatform([]*Group{
		{ID: 3, Platform: PlatformOpenAI, Kind: GroupKindManaged},
		{ID: 4, Platform: PlatformOpenAI},
	})
	require.ErrorIs(t, err, ErrManagedGroupAccountExclusive)
}
