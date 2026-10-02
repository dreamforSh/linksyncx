import type { GroupKind } from '@/types'
import { isManagedGroup } from './groupKind'

export interface TargetGroupLike {
  id: number
  platform: string
  kind?: GroupKind | string | null
}

export type TargetGroupProblem = { kind: 'managed-exclusive'; platform: string }

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
 * 多平台导入（JSON 导入、CRS 同步）的前置校验，与后端规则对齐：目标分组可选（没有对应
 * 平台分组的账号导入为未分组账号），但同一平台选了管理分组时，它必须是该平台唯一的目标分组。
 */
export function findTargetGroupProblem(groupIds: number[], groups: TargetGroupLike[]): TargetGroupProblem | null {
  for (const [platform, list] of groupTargetsByPlatform(groupIds, groups)) {
    if (list.length > 1 && list.some(group => isManagedGroup(group))) {
      return { kind: 'managed-exclusive', platform }
    }
  }
  return null
}
