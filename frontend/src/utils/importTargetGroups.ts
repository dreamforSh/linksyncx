import type { GroupKind } from '@/types'
import { isManagedGroup } from './groupKind'

export interface TargetGroupLike {
  id: number
  platform: string
  kind?: GroupKind | string | null
}

export type TargetGroupProblem =
  | { kind: 'required' }
  | { kind: 'managed-exclusive'; platform: string }
  | { kind: 'missing'; platforms: string[] }

/** 把已选目标分组按平台分桶（与后端 GroupIDsByPlatform 一致，按分组平台精确匹配）。 */
export function groupTargetsByPlatform<T extends TargetGroupLike>(groupIds: number[], groups: T[]): Map<string, T[]> {
  const selected = new Set(groupIds)
  const byPlatform = new Map<string, T[]>()
  for (const group of groups) {
    if (!selected.has(group.id)) continue
    const list = byPlatform.get(group.platform) ?? []
    list.push(group)
    byPlatform.set(group.platform, list)
  }
  return byPlatform
}

/**
 * 多平台导入（JSON 导入、CRS 同步）的前置校验，与后端规则对齐：
 * - 同一平台选了管理分组时，它必须是该平台唯一的目标分组；
 * - 有账号要创建时必须选目标分组；
 * - 账号涉及的每个平台都要有目标分组，否则这些账号不会被创建。
 *
 * accountPlatforms 为待创建账号的平台列表（每个账号一项，可重复）。
 */
export function findTargetGroupProblem(
  accountPlatforms: string[],
  groupIds: number[],
  groups: TargetGroupLike[]
): TargetGroupProblem | null {
  const byPlatform = groupTargetsByPlatform(groupIds, groups)
  for (const [platform, list] of byPlatform) {
    if (list.length > 1 && list.some(group => isManagedGroup(group))) {
      return { kind: 'managed-exclusive', platform }
    }
  }
  if (!accountPlatforms.length) return null
  if (!groupIds.length) return { kind: 'required' }
  const missing = [...new Set(accountPlatforms.filter(Boolean))].filter(platform => !byPlatform.get(platform)?.length)
  return missing.length ? { kind: 'missing', platforms: missing } : null
}
