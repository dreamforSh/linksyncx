// 后台“重启”走后端的优雅关停：监听立即关闭 → 进行中的请求最多再等
// server.shutdown_timeout_seconds（默认 30s）→ 强制取消约 5s → 依赖清理最多 30s →
// systemd/Docker 拉起新进程并完成启动。固定的短倒计时会在旧进程仍在排空、
// 新进程尚未就绪时刷新页面，落到错误页。
//
// 所以先确认旧进程已经下线（/health 失败），再等新进程恢复健康；两个阶段都有上限，
// 超时后无论如何刷新，保持原有“最终总会刷新”的行为。

/** 轮询 /health 的间隔。 */
export const RESTART_POLL_INTERVAL_MS = 1000
/**
 * 等旧进程下线的上限。后端在重启请求返回约 0.6s 后就关闭监听，超过这个时间
 * 仍然健康，说明重启没有发生（例如非 Linux 部署会忽略重启请求），或下线窗口
 * 短到恰好落在两次探测之间、新进程已就绪——两种情况都应直接刷新。
 */
export const RESTART_DOWN_DETECTION_LIMIT_MS = 15_000
/** 整体上限：宽限 30s + 强制 5s + 清理 30s + 拉起与启动，留足余量。 */
export const RESTART_TOTAL_LIMIT_MS = 120_000
/** 单次探测超时：反向代理在后端下线时可能挂起请求，不能让一次探测卡住轮询。 */
export const RESTART_PROBE_TIMEOUT_MS = 3000

export type ServiceRestartWaitResult =
  /** 观测到下线后又恢复健康：新进程已就绪。 */
  | 'restarted'
  /** 上限内从未观测到下线。 */
  | 'still-up'
  /** 超过整体上限仍未恢复。 */
  | 'timeout'
  /** 调用方已放弃等待（例如组件卸载）。 */
  | 'cancelled'

export interface ServiceRestartWaitOptions {
  /** 健康返回 true；网络错误、超时、非 2xx（含排空期间的 503、代理的 502）返回 false。 */
  probe: () => Promise<boolean>
  sleep?: (ms: number) => Promise<void>
  now?: () => number
  isCancelled?: () => boolean
  intervalMs?: number
  downDetectionLimitMs?: number
  totalLimitMs?: number
}

const defaultSleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))

export async function waitForServiceRestart(
  options: ServiceRestartWaitOptions
): Promise<ServiceRestartWaitResult> {
  const sleep = options.sleep ?? defaultSleep
  const now = options.now ?? Date.now
  const intervalMs = options.intervalMs ?? RESTART_POLL_INTERVAL_MS
  const downDetectionLimitMs = options.downDetectionLimitMs ?? RESTART_DOWN_DETECTION_LIMIT_MS
  const totalLimitMs = options.totalLimitMs ?? RESTART_TOTAL_LIMIT_MS
  const startedAt = now()
  let sawDown = false

  for (;;) {
    if (options.isCancelled?.()) return 'cancelled'
    const healthy = await options.probe()
    if (options.isCancelled?.()) return 'cancelled'
    const elapsed = now() - startedAt
    if (!sawDown) {
      if (!healthy) {
        sawDown = true
      } else if (elapsed >= downDetectionLimitMs) {
        return 'still-up'
      }
    } else if (healthy) {
      return 'restarted'
    }
    if (elapsed >= totalLimitMs) return 'timeout'
    await sleep(intervalMs)
  }
}

export async function probeServiceHealth(
  fetchImpl: typeof fetch = fetch,
  timeoutMs: number = RESTART_PROBE_TIMEOUT_MS
): Promise<boolean> {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeoutMs)
  try {
    const response = await fetchImpl('/health', {
      method: 'GET',
      cache: 'no-store',
      signal: controller.signal
    })
    return response.ok
  } catch {
    return false
  } finally {
    clearTimeout(timer)
  }
}
