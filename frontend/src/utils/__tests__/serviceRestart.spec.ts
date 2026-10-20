import { describe, expect, it, vi } from 'vitest'
import {
  probeServiceHealth,
  RESTART_DOWN_DETECTION_LIMIT_MS,
  RESTART_TOTAL_LIMIT_MS,
  restartLimitMinutes,
  restartTotalLimitMs,
  waitForServiceRestart
} from '../serviceRestart'

// 用模拟时钟驱动轮询：sleep 只推进时间，不真正等待。
const simulatedClock = () => {
  let current = 0
  return {
    now: () => current,
    sleep: async (ms: number) => {
      current += ms
    }
  }
}

describe('waitForServiceRestart', () => {
  it('旧进程排空期间不刷新，观测到下线后等新进程恢复健康', async () => {
    const clock = simulatedClock()
    // 0~1s 旧进程仍健康（重启请求返回后约 0.6s 才开始关停），随后下线 70s
    // （宽限 30s + 强制 5s + 清理 + 拉起启动），之后新进程健康。
    const probe = vi.fn(async () => clock.now() < 1000 || clock.now() >= 71_000)

    await expect(waitForServiceRestart({ probe, ...clock })).resolves.toBe('restarted')
    expect(clock.now()).toBeGreaterThanOrEqual(71_000)
    expect(clock.now()).toBeLessThan(73_000)
  })

  it('一直健康时在下线检测上限后返回 still-up', async () => {
    const clock = simulatedClock()
    const probe = vi.fn(async () => true)

    await expect(waitForServiceRestart({ probe, ...clock })).resolves.toBe('still-up')
    expect(clock.now()).toBe(RESTART_DOWN_DETECTION_LIMIT_MS)
  })

  it('一直未恢复时在整体上限后返回 timeout', async () => {
    const clock = simulatedClock()
    const probe = vi.fn(async () => false)

    await expect(waitForServiceRestart({ probe, ...clock })).resolves.toBe('timeout')
    expect(clock.now()).toBe(RESTART_TOTAL_LIMIT_MS)
  })

  it('取消后立即停止轮询', async () => {
    const clock = simulatedClock()
    let cancelled = false
    const probe = vi.fn(async () => {
      cancelled = clock.now() >= 3000
      return false
    })

    await expect(
      waitForServiceRestart({ probe, ...clock, isCancelled: () => cancelled })
    ).resolves.toBe('cancelled')
    expect(probe).toHaveBeenCalledTimes(4)
  })
})

describe('restartTotalLimitMs', () => {
  it('按后端下发的宽限放宽整体上限：宽限 + 强制 5s + 清理 30s + 拉起启动 60s', () => {
    expect(restartTotalLimitMs(300)).toBe(395_000)
    expect(restartTotalLimitMs(3600)).toBe(3_695_000)
    expect(restartTotalLimitMs(30)).toBe(125_000)
  })

  it('字段缺失或无效时退回默认上限，小宽限也不缩短原有等待', () => {
    expect(restartTotalLimitMs()).toBe(RESTART_TOTAL_LIMIT_MS)
    expect(restartTotalLimitMs(null)).toBe(RESTART_TOTAL_LIMIT_MS)
    expect(restartTotalLimitMs(0)).toBe(RESTART_TOTAL_LIMIT_MS)
    expect(restartTotalLimitMs(-5)).toBe(RESTART_TOTAL_LIMIT_MS)
    expect(restartTotalLimitMs(Number.NaN)).toBe(RESTART_TOTAL_LIMIT_MS)
    expect(restartTotalLimitMs(1)).toBe(RESTART_TOTAL_LIMIT_MS)
  })

  it('宽限 300s 时旧进程排空 5 分多钟，按宽限放宽的上限能等到新进程，固定上限会提前刷新', async () => {
    // 0~1s 旧进程仍健康，随后排空 300s + 强制 5s + 清理 + 拉起启动，约 331s 时新进程健康。
    const downUntil = 331_000
    const fixed = simulatedClock()
    await expect(
      waitForServiceRestart({
        probe: async () => fixed.now() < 1000 || fixed.now() >= downUntil,
        ...fixed
      })
    ).resolves.toBe('timeout')
    expect(fixed.now()).toBe(RESTART_TOTAL_LIMIT_MS)

    const scaled = simulatedClock()
    await expect(
      waitForServiceRestart({
        probe: async () => scaled.now() < 1000 || scaled.now() >= downUntil,
        ...scaled,
        totalLimitMs: restartTotalLimitMs(300)
      })
    ).resolves.toBe('restarted')
    expect(scaled.now()).toBeGreaterThanOrEqual(downUntil)
  })
})

describe('restartLimitMinutes', () => {
  it('提示里的分钟数随上限变化，默认宽限仍是约 2 分钟', () => {
    expect(restartLimitMinutes(RESTART_TOTAL_LIMIT_MS)).toBe(2)
    expect(restartLimitMinutes(restartTotalLimitMs(30))).toBe(2)
    expect(restartLimitMinutes(restartTotalLimitMs(300))).toBe(7)
    expect(restartLimitMinutes(restartTotalLimitMs(3600))).toBe(62)
  })
})

describe('probeServiceHealth', () => {
  const respond = (status: number) =>
    vi.fn(async () => new Response('{}', { status })) as unknown as typeof fetch

  it('只有 2xx 视为健康，排空期间的 503 与代理 502 视为下线', async () => {
    await expect(probeServiceHealth(respond(200))).resolves.toBe(true)
    await expect(probeServiceHealth(respond(503))).resolves.toBe(false)
    await expect(probeServiceHealth(respond(502))).resolves.toBe(false)
  })

  it('网络错误视为下线', async () => {
    const failing = vi.fn(async () => {
      throw new TypeError('Failed to fetch')
    }) as unknown as typeof fetch
    await expect(probeServiceHealth(failing)).resolves.toBe(false)
  })

  it('挂起的探测在超时后视为下线，且请求 /health 时不使用缓存', async () => {
    const hanging = vi.fn(
      (_input: RequestInfo | URL, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
        })
    )
    await expect(probeServiceHealth(hanging as unknown as typeof fetch, 10)).resolves.toBe(false)
    expect(hanging).toHaveBeenCalledWith('/health', expect.objectContaining({ cache: 'no-store' }))
  })
})
