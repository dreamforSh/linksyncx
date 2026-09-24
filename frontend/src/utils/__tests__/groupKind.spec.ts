import { describe, expect, it } from 'vitest'

import { findTargetGroupProblem, groupTargetsByPlatform } from '../importTargetGroups'
import { groupKindOf, isGroupCategory, isGroupKind, isManagedGroup, violatesManagedExclusivity } from '../groupKind'

const channelA = { id: 1, platform: 'anthropic', kind: 'channel' as const }
const channelB = { id: 2, platform: 'anthropic' }
const openai = { id: 3, platform: 'openai', kind: 'channel' as const }
const managed = { id: 4, platform: 'anthropic', kind: 'managed' as const }

describe('groupKind', () => {
  it('treats groups without kind as channel groups', () => {
    expect(groupKindOf(channelB)).toBe('channel')
    expect(groupKindOf(null)).toBe('channel')
    expect(groupKindOf(managed)).toBe('managed')
    expect(isManagedGroup(managed)).toBe(true)
    expect(isManagedGroup(channelA)).toBe(false)
  })

  it('guards kind and category values', () => {
    expect(isGroupKind('managed')).toBe(true)
    expect(isGroupKind('composite')).toBe(false)
    expect(isGroupCategory('team')).toBe(true)
    expect(isGroupCategory('')).toBe(false)
  })

  it('flags a managed group combined with any other group', () => {
    const groups = [channelA, channelB, managed]
    expect(violatesManagedExclusivity([4], groups)).toBe(false)
    expect(violatesManagedExclusivity([1, 2], groups)).toBe(false)
    expect(violatesManagedExclusivity([1, 4], groups)).toBe(true)
  })
})

describe('importTargetGroups', () => {
  const groups = [channelA, channelB, openai, managed]

  it('buckets selected groups by platform', () => {
    const byPlatform = groupTargetsByPlatform([1, 3], groups)
    expect(byPlatform.get('anthropic')?.map(group => group.id)).toEqual([1])
    expect(byPlatform.get('openai')?.map(group => group.id)).toEqual([3])
  })

  it('needs no target group when nothing will be created', () => {
    expect(findTargetGroupProblem([], [], groups)).toBeNull()
  })

  it('requires target groups when accounts will be created', () => {
    expect(findTargetGroupProblem(['anthropic'], [], groups)).toEqual({ kind: 'required' })
  })

  it('reports platforms without any selected group', () => {
    expect(findTargetGroupProblem(['anthropic', 'openai', 'openai'], [1], groups)).toEqual({
      kind: 'missing',
      platforms: ['openai']
    })
    expect(findTargetGroupProblem(['anthropic', 'openai'], [1, 3], groups)).toBeNull()
  })

  it('rejects a managed group that shares its platform with other targets', () => {
    expect(findTargetGroupProblem(['anthropic'], [1, 4], groups)).toEqual({
      kind: 'managed-exclusive',
      platform: 'anthropic'
    })
    // 管理分组独占按平台判断：其他平台的目标分组不受影响
    expect(findTargetGroupProblem(['anthropic', 'openai'], [4, 3], groups)).toBeNull()
  })
})
