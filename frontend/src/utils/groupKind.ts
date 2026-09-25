import type { GroupCategory, GroupKind } from '@/types'

interface KindLike {
  kind?: GroupKind | string | null
}

export const GROUP_KINDS: readonly GroupKind[] = ['channel', 'managed']
export const GROUP_CATEGORIES: readonly GroupCategory[] = ['enterprise', 'team']

// 旧后端不返回 kind，一律按渠道分组处理。
export function groupKindOf(group?: KindLike | null): GroupKind {
  return group?.kind === 'managed' ? 'managed' : 'channel'
}

export function isManagedGroup(group?: KindLike | null): boolean {
  return groupKindOf(group) === 'managed'
}

export function isGroupKind(value: unknown): value is GroupKind {
  return value === 'channel' || value === 'managed'
}

export function isGroupCategory(value: unknown): value is GroupCategory {
  return value === 'enterprise' || value === 'team'
}

/**
 * 校验一组待绑定分组是否满足「管理分组独占」：选中管理分组时它必须是唯一的分组。
 * 返回 true 表示违规。
 */
export function violatesManagedExclusivity(groupIds: number[], groups: Array<KindLike & { id: number }>): boolean {
  if (groupIds.length <= 1) return false
  const selected = new Set(groupIds)
  return groups.some(group => selected.has(group.id) && isManagedGroup(group))
}
