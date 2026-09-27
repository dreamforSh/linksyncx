import { beforeEach, describe, expect, it, vi } from 'vitest'

const clash = vi.hoisted(() => ({ testNodesLatency: vi.fn(), probeNodesExit: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { clash } }))

import { useClashBatchTest } from '../useClashBatchTest'

const latencyOk = (ids: number[]) => ids.map((id) => ({ node_id: id, success: true, latency_ms: 10, health_status: 'healthy' }))

beforeEach(() => {
  vi.clearAllMocks()
})

describe('useClashBatchTest', () => {
  it('tests latency in chunks of 10 and counts nodes the server skipped', async () => {
    // The server leaves out nodes it will not test (disabled, missing).
    clash.testNodesLatency.mockImplementation(async ({ node_ids }: { node_ids: number[] }) =>
      latencyOk(node_ids.filter((id) => id !== 3)).map((result) => (result.node_id === 5 ? { ...result, success: false } : result)))
    const onLatency = vi.fn()
    const batch = useClashBatchTest({ onLatency })
    const ids = Array.from({ length: 12 }, (_, index) => index + 1)

    const result = await batch.run('latency', ids)

    expect(clash.testNodesLatency).toHaveBeenCalledTimes(2)
    expect(clash.testNodesLatency).toHaveBeenNthCalledWith(1, { node_ids: ids.slice(0, 10) })
    expect(clash.testNodesLatency).toHaveBeenNthCalledWith(2, { node_ids: ids.slice(10) })
    expect(onLatency).toHaveBeenCalledTimes(2)
    expect(result).toMatchObject({ kind: 'latency', total: 12, done: 12, success: 10, failed: 1, skipped: 1, running: false, cancelled: false })
    expect(clash.probeNodesExit).not.toHaveBeenCalled()
  })

  it('counts exit probe outcomes and pending exit changes', async () => {
    clash.probeNodesExit.mockResolvedValue([
      { node_id: 1, success: true, exit_status: 'ok', exit_ip: '1.1.1.1' },
      { node_id: 2, success: true, exit_status: 'changed', exit_ip: '2.2.2.2' },
      { node_id: 3, success: false, exit_status: 'stale', error: 'timeout' }
    ])
    const onExit = vi.fn()
    const batch = useClashBatchTest({ onExit })

    const result = await batch.run('exit', [1, 2, 3])

    expect(clash.testNodesLatency).not.toHaveBeenCalled()
    expect(onExit).toHaveBeenCalledOnce()
    expect(result).toMatchObject({ success: 2, failed: 1, changed: 1, skipped: 0, done: 3 })
  })

  it('probes only the nodes whose delay test passed in a full check', async () => {
    clash.testNodesLatency.mockResolvedValue([
      { node_id: 1, success: true, latency_ms: 10, health_status: 'healthy' },
      { node_id: 2, success: false, error: 'timeout', health_status: 'unknown' }
    ])
    // A passed node the probe does not report still counts as a failure.
    clash.probeNodesExit.mockResolvedValue([])
    const batch = useClashBatchTest()

    const result = await batch.run('full', [1, 2])

    expect(clash.probeNodesExit).toHaveBeenCalledWith({ node_ids: [1] })
    expect(result).toMatchObject({ success: 0, failed: 2, done: 2 })
  })

  it('fails a whole chunk when a request errors and keeps going', async () => {
    clash.testNodesLatency
      .mockRejectedValueOnce(new Error('network'))
      .mockImplementation(async ({ node_ids }: { node_ids: number[] }) => latencyOk(node_ids))
    clash.probeNodesExit.mockRejectedValue(new Error('core down'))
    const batch = useClashBatchTest()

    const result = await batch.run('full', Array.from({ length: 7 }, (_, index) => index + 1))

    // Chunk 1 (5 nodes) fails at the delay test; chunk 2 passes it and fails at the probe.
    expect(result).toMatchObject({ total: 7, done: 7, success: 0, failed: 7 })
  })

  it('ignores a second run while one is in progress and can be dismissed afterwards', async () => {
    let release!: () => void
    clash.testNodesLatency.mockImplementation(
      ({ node_ids }: { node_ids: number[] }) => new Promise((resolve) => { release = () => resolve(latencyOk(node_ids)) })
    )
    const batch = useClashBatchTest()

    const first = batch.run('latency', [1])
    expect(batch.running.value).toBe(true)
    await expect(batch.run('latency', [2])).resolves.toBeNull()
    batch.dismiss()
    expect(batch.progress.value).not.toBeNull()

    release()
    await first
    expect(batch.running.value).toBe(false)
    batch.dismiss()
    expect(batch.progress.value).toBeNull()
  })
})
