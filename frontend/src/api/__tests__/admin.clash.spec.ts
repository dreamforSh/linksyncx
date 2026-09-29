import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, put, del } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  del: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, post, put, delete: del }
}))

import {
  CLASH_EXIT_PROBE_TIMEOUT_MS,
  CLASH_LATENCY_TIMEOUT_MS,
  CLASH_NODE_ACTION_LIMIT,
  CLASH_NODE_BATCH_LIMIT,
  CLASH_RESYNC_TIMEOUT_MS,
  CLASH_SUBSCRIPTION_TIMEOUT_MS,
  acceptNodeExit,
  createProfile,
  deleteProfile,
  disableNode,
  enableNode,
  getProfile,
  getRuntime,
  getSettings,
  listExits,
  listNodeIds,
  listNodes,
  listProfiles,
  previewProfile,
  probeNodesExit,
  refreshProfile,
  resyncRuntime,
  testNodesLatency,
  updateNodes,
  updateProfile,
  updateSettings
} from '@/api/admin/clash'
import { adminAPI, clashAPI } from '@/api/admin'
import { makeClashPoolSettings } from '@/__tests__/fixtures/clash'

beforeEach(() => {
  for (const fn of [get, post, put, del]) {
    fn.mockReset()
    fn.mockResolvedValue({ data: {} })
  }
})

describe('admin Clash API — subscriptions', () => {
  it('is registered on the admin API barrel', () => {
    expect(adminAPI.clash).toBe(clashAPI)
    expect(clashAPI.listExits).toBe(listExits)
  })

  it('lists profiles and tolerates an empty payload', async () => {
    get.mockResolvedValueOnce({ data: [{ id: 1 }] })
    await expect(listProfiles()).resolves.toEqual([{ id: 1 }])
    expect(get).toHaveBeenLastCalledWith('/admin/clash/profiles')

    get.mockResolvedValueOnce({ data: null })
    await expect(listProfiles()).resolves.toEqual([])
  })

  it('gets a single profile', async () => {
    await getProfile(7)
    expect(get).toHaveBeenCalledWith('/admin/clash/profiles/7')
  })

  it('creates a profile with the subscription timeout', async () => {
    const body = { name: 'A', url: 'https://sub.example/a' }
    await createProfile(body)
    expect(post).toHaveBeenCalledWith('/admin/clash/profiles', body, { timeout: CLASH_SUBSCRIPTION_TIMEOUT_MS })
    expect(CLASH_SUBSCRIPTION_TIMEOUT_MS).toBe(120_000)
  })

  it('updates a profile with PUT and the subscription timeout (a new file may be uploaded)', async () => {
    await updateProfile(3, { enabled: false, fetch_proxy_id: 0 })
    expect(put).toHaveBeenCalledWith('/admin/clash/profiles/3', { enabled: false, fetch_proxy_id: 0 }, {
      timeout: CLASH_SUBSCRIPTION_TIMEOUT_MS
    })
  })

  it('creates and previews file subscriptions with the file content', async () => {
    const body = { name: 'F', source_type: 'file' as const, content: 'proxies: []', source_name: 'a.yaml' }
    await createProfile(body)
    expect(post).toHaveBeenLastCalledWith('/admin/clash/profiles', body, { timeout: CLASH_SUBSCRIPTION_TIMEOUT_MS })
    const preview = { source_type: 'file' as const, content: 'proxies: []' }
    await previewProfile(preview)
    expect(post).toHaveBeenLastCalledWith('/admin/clash/profiles/preview', preview, { timeout: CLASH_SUBSCRIPTION_TIMEOUT_MS })
  })

  it('deletes with and without force', async () => {
    await deleteProfile(4)
    expect(del).toHaveBeenLastCalledWith('/admin/clash/profiles/4', { params: undefined })
    await deleteProfile(4, { force: true })
    expect(del).toHaveBeenLastCalledWith('/admin/clash/profiles/4', { params: { force: 'true' } })
  })

  it('refreshes with the subscription timeout and optional force', async () => {
    await refreshProfile(5)
    expect(post).toHaveBeenLastCalledWith('/admin/clash/profiles/5/refresh', undefined, {
      params: undefined,
      timeout: CLASH_SUBSCRIPTION_TIMEOUT_MS
    })
    await refreshProfile(5, { force: true })
    expect(post).toHaveBeenLastCalledWith('/admin/clash/profiles/5/refresh', undefined, {
      params: { force: 'true' },
      timeout: CLASH_SUBSCRIPTION_TIMEOUT_MS
    })
  })

  it('previews a subscription with the subscription timeout', async () => {
    const body = { url: 'https://sub.example/a', exclude_pattern: '' }
    await previewProfile(body)
    expect(post).toHaveBeenCalledWith('/admin/clash/profiles/preview', body, { timeout: CLASH_SUBSCRIPTION_TIMEOUT_MS })
  })
})

describe('admin Clash API — nodes', () => {
  it('lists nodes with only the active filters', async () => {
    const controller = new AbortController()
    await listNodes(2, 50, { profile_id: 9, status: 'active', health: 'unhealthy', bound: false, search: '  hk  ' }, {
      signal: controller.signal
    })
    expect(get).toHaveBeenCalledWith('/admin/clash/nodes', {
      params: { page: 2, page_size: 50, profile_id: 9, status: 'active', health: 'unhealthy', bound: 'false', search: 'hk' },
      signal: controller.signal
    })

    await listNodes(1, 20, { bound: true, search: '   ' })
    expect(get).toHaveBeenLastCalledWith('/admin/clash/nodes', {
      params: { page: 1, page_size: 20, bound: 'true' },
      signal: undefined
    })

    await listNodes(1, 20, { sort: 'traffic_today' })
    expect(get).toHaveBeenLastCalledWith('/admin/clash/nodes', {
      params: { page: 1, page_size: 20, sort: 'traffic_today' },
      signal: undefined
    })
  })

  it('sends the visibility filter only when it is not the default', async () => {
    await listNodes(1, 20, { visibility: 'hidden' })
    expect(get).toHaveBeenLastCalledWith('/admin/clash/nodes', {
      params: { page: 1, page_size: 20, visibility: 'hidden' },
      signal: undefined
    })
    await listNodes(1, 20, { visibility: 'visible' })
    expect(get).toHaveBeenLastCalledWith('/admin/clash/nodes', { params: { page: 1, page_size: 20 }, signal: undefined })
    await listNodeIds({ visibility: 'all' })
    expect(get).toHaveBeenLastCalledWith('/admin/clash/nodes/ids', { params: { visibility: 'all' } })
  })

  it('applies node actions in server-sized chunks and sums the results', async () => {
    const ids = Array.from({ length: CLASH_NODE_ACTION_LIMIT + 2 }, (_, index) => index + 1)
    post.mockImplementation(async (_url: string, body: { action: string; node_ids: number[] }) => ({
      data: { action: body.action, updated: body.node_ids.length - 1, skipped: 1, node_ids: body.node_ids.slice(1) }
    }))

    const result = await updateNodes('hide', ids)

    expect(post).toHaveBeenCalledTimes(2)
    expect(post).toHaveBeenNthCalledWith(1, '/admin/clash/nodes/batch', { action: 'hide', node_ids: ids.slice(0, CLASH_NODE_ACTION_LIMIT) })
    expect(post).toHaveBeenNthCalledWith(2, '/admin/clash/nodes/batch', { action: 'hide', node_ids: ids.slice(CLASH_NODE_ACTION_LIMIT) })
    expect(result).toEqual({
      action: 'hide',
      updated: ids.length - 2,
      skipped: 2,
      node_ids: [...ids.slice(1, CLASH_NODE_ACTION_LIMIT), ...ids.slice(CLASH_NODE_ACTION_LIMIT + 1)]
    })
    expect(clashAPI.updateNodes).toBe(updateNodes)

    post.mockClear()
    await expect(updateNodes('unhide', [])).resolves.toEqual({ action: 'unhide', updated: 0, skipped: 0, node_ids: [] })
    expect(post).not.toHaveBeenCalled()
  })

  it('lists the ids of every filtered node for batch tests', async () => {
    get.mockResolvedValueOnce({ data: { ids: [3, 1, 2] } })
    await expect(listNodeIds({ profile_id: 9, bound: false, sort: 'latency' }, { live: true })).resolves.toEqual([3, 1, 2])
    expect(get).toHaveBeenLastCalledWith('/admin/clash/nodes/ids', {
      params: { profile_id: 9, bound: 'false', sort: 'latency', live: 'true' }
    })
    expect(clashAPI.listNodeIds).toBe(listNodeIds)

    get.mockResolvedValueOnce({ data: null })
    await expect(listNodeIds()).resolves.toEqual([])
    expect(get).toHaveBeenLastCalledWith('/admin/clash/nodes/ids', { params: {} })
  })

  it('toggles nodes and accepts exit changes', async () => {
    await enableNode(11)
    expect(post).toHaveBeenLastCalledWith('/admin/clash/nodes/11/enable')
    await disableNode(11)
    expect(post).toHaveBeenLastCalledWith('/admin/clash/nodes/11/disable')
    await acceptNodeExit(12)
    expect(post).toHaveBeenLastCalledWith('/admin/clash/nodes/12/accept-exit')
  })

  it('tests latency in server-sized batches and concatenates the results', async () => {
    const ids = Array.from({ length: CLASH_NODE_BATCH_LIMIT + 5 }, (_, index) => index + 1)
    post.mockImplementation(async (_url: string, body: { node_ids: number[] }) => ({
      data: body.node_ids.map((id) => ({ node_id: id, success: true, health_status: 'healthy' }))
    }))

    const results = await testNodesLatency({ node_ids: ids })

    expect(post).toHaveBeenCalledTimes(2)
    expect(post).toHaveBeenNthCalledWith(1, '/admin/clash/nodes/test-latency', { node_ids: ids.slice(0, CLASH_NODE_BATCH_LIMIT) }, {
      timeout: CLASH_LATENCY_TIMEOUT_MS
    })
    expect(post).toHaveBeenNthCalledWith(2, '/admin/clash/nodes/test-latency', { node_ids: ids.slice(CLASH_NODE_BATCH_LIMIT) }, {
      timeout: CLASH_LATENCY_TIMEOUT_MS
    })
    expect(results.map((result) => result.node_id)).toEqual(ids)
  })

  it('tests a whole profile in one request and skips empty selections', async () => {
    post.mockResolvedValue({ data: [] })
    await testNodesLatency({ profile_id: 3 })
    expect(post).toHaveBeenCalledWith('/admin/clash/nodes/test-latency', { profile_id: 3 }, { timeout: CLASH_LATENCY_TIMEOUT_MS })

    post.mockClear()
    await expect(testNodesLatency({ node_ids: [] })).resolves.toEqual([])
    expect(post).not.toHaveBeenCalled()
  })

  it('probes exits with the long probe timeout', async () => {
    post.mockResolvedValue({ data: [{ node_id: 1, success: true, exit_status: 'ok' }] })
    await probeNodesExit({ node_ids: [1] })
    expect(post).toHaveBeenCalledWith('/admin/clash/nodes/probe-exit', { node_ids: [1] }, { timeout: CLASH_EXIT_PROBE_TIMEOUT_MS })
    expect(CLASH_EXIT_PROBE_TIMEOUT_MS).toBe(180_000)
  })
})

describe('admin Clash API — exits, runtime and settings', () => {
  it('reads exits and runtime', async () => {
    await listExits()
    expect(get).toHaveBeenLastCalledWith('/admin/clash/exits')
    await getRuntime()
    expect(get).toHaveBeenLastCalledWith('/admin/clash/runtime')
  })

  it('resyncs the runtime with its own timeout', async () => {
    await resyncRuntime()
    expect(post).toHaveBeenCalledWith('/admin/clash/runtime/resync', undefined, { timeout: CLASH_RESYNC_TIMEOUT_MS })
  })

  it('reads and writes pool settings', async () => {
    await getSettings()
    expect(get).toHaveBeenLastCalledWith('/admin/clash/settings')
    const settings = makeClashPoolSettings({ max_accounts_per_exit: 2, automatic_probes_enabled: false })
    await updateSettings(settings)
    expect(put).toHaveBeenCalledWith('/admin/clash/settings', settings)
  })
})
