import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const { getSummary } = vi.hoisted(() => ({ getSummary: vi.fn() }))

vi.mock('@/api/groupManagement', () => ({
  groupManagementAPI: { getSummary }
}))

import { useGroupManagementStore } from '../groupManagement'

describe('group management summary store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getSummary.mockReset()
  })

  it('loads the summary once per user and refetches when the user changes', async () => {
    getSummary.mockResolvedValue({ role: 'user', managed_group_count: 0, membership_count: 2 })
    const store = useGroupManagementStore()

    await Promise.all([store.ensureSummary(7), store.ensureSummary(7)])
    await store.ensureSummary(7)
    expect(getSummary).toHaveBeenCalledTimes(1)
    expect(store.summary?.membership_count).toBe(2)

    await store.ensureSummary(8)
    expect(getSummary).toHaveBeenCalledTimes(2)

    await store.ensureSummary(8, true)
    expect(getSummary).toHaveBeenCalledTimes(3)
  })

  it('keeps the entry hidden when the summary cannot be loaded', async () => {
    getSummary.mockRejectedValue(new Error('offline'))
    const store = useGroupManagementStore()

    await store.ensureSummary(7)
    expect(store.summary).toBeNull()
    // 失败后同一用户不再反复请求
    await store.ensureSummary(7)
    expect(getSummary).toHaveBeenCalledTimes(1)
  })

  it('clears the summary without a user', async () => {
    getSummary.mockResolvedValue({ role: 'user', managed_group_count: 0, membership_count: 1 })
    const store = useGroupManagementStore()
    await store.ensureSummary(7)

    await store.ensureSummary(null)
    expect(store.summary).toBeNull()
  })
})
