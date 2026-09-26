import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))

import { getAll, getAllWithCount, list } from '@/api/admin/proxies'

describe.each([
  { name: 'paginated list', load: () => list(), wrap: (items: unknown[]) => ({ items, total: items.length, pages: 1 }) },
  { name: 'account selector', load: getAll, wrap: (items: unknown[]) => items },
  { name: 'account selector with counts', load: getAllWithCount, wrap: (items: unknown[]) => items },
])('$name', ({ load, wrap }) => {
  beforeEach(() => { get.mockReset() })

  it.each(['', undefined, null, {}, { items: null }, { items: {} }])(
    'rejects an empty or malformed successful response: %j',
    async (data) => {
      get.mockResolvedValue({ data })
      await expect(load()).rejects.toThrow('Invalid proxy list response')
    },
  )

  it.each([{ items: [] }, { items: [{ id: 1, name: 'valid proxy' }] }])('preserves valid rows: %j', async ({ items }) => {
    const data = wrap(items)
    get.mockResolvedValue({ data })
    await expect(load()).resolves.toBe(data)
  })
})

describe('paginated list source filter', () => {
  beforeEach(() => { get.mockReset() })

  it.each(['manual', 'clash', 'all'] as const)('passes source=%s through to the query', async (source) => {
    const data = { items: [], total: 0, pages: 1 }
    get.mockResolvedValue({ data })
    const controller = new AbortController()

    await list(2, 50, { status: 'active', source }, { signal: controller.signal })

    expect(get).toHaveBeenCalledWith('/admin/proxies', {
      params: { page: 2, page_size: 50, status: 'active', source },
      signal: controller.signal
    })
  })

  it('omits source when the caller does not filter by it (server defaults to manual)', async () => {
    get.mockResolvedValue({ data: { items: [], total: 0, pages: 1 } })
    await list(1, 20, { search: 'hk' })
    expect(get.mock.lastCall?.[1].params).not.toHaveProperty('source')
  })
})
