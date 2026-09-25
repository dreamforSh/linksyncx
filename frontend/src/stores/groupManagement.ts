/**
 * Group management summary store
 * 侧栏据此决定是否向普通用户显示「组管理」入口：只有属于某个分组的用户才显示。
 * 按登录用户缓存 60 秒：分组被删除或成员被移除后，切换页面时很快就会刷新入口。
 */

import { defineStore } from 'pinia'
import { ref } from 'vue'
import { groupManagementAPI, type GroupManagementSummary } from '@/api/groupManagement'

const SUMMARY_TTL_MS = 60_000

export const useGroupManagementStore = defineStore('groupManagement', () => {
  const summary = ref<GroupManagementSummary | null>(null)
  const loadedFor = ref<number | null>(null)
  const loadedAt = ref(0)
  let pending: Promise<void> | null = null

  async function ensureSummary(userId: number | null | undefined, force = false): Promise<void> {
    if (!userId) {
      reset()
      return
    }
    if (!force && loadedFor.value === userId && Date.now() - loadedAt.value < SUMMARY_TTL_MS) return
    if (pending) return pending
    pending = (async () => {
      try {
        summary.value = await groupManagementAPI.getSummary()
      } catch {
        // 入口只是便捷导航，拉取失败时保持隐藏，避免每次切页都重试
        summary.value = null
      } finally {
        loadedFor.value = userId
        loadedAt.value = Date.now()
        pending = null
      }
    })()
    return pending
  }

  function reset() {
    summary.value = null
    loadedFor.value = null
    loadedAt.value = 0
  }

  return { summary, ensureSummary, reset }
})
