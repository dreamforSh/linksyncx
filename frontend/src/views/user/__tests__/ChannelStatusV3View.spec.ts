import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick, type ComponentPublicInstance } from 'vue'
import type { MonitorCoverage, MonitorHealth, MonitorMatrixResponse, MonitorMetric, MonitorSnapshot } from '@/api/channelMonitorV2'

const messages: Record<string, string> = {
  'monitorCommon.nextUpdateIn': '{n}s 后刷新',
  'channelMonitorV3.summary': '成功率 {success} · 缓存率 {cache}',
  'channelMonitorV2.matrix.noTrafficAt': '{time} · 无流量',
  'channelMonitorV3.timelineTooltip': '{time} · 可用率 {availability}',
}
const t = (key: string, params?: Record<string, unknown>) =>
  (messages[key] ?? key).replace(/\{(\w+)\}/g, (_, name) => String(params?.[name] ?? ''))

const { api, showError } = vi.hoisted(() => ({
  api: { getSnapshot: vi.fn(), getMatrix: vi.fn(), getDimensions: vi.fn() },
  showError: vi.fn(),
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t, te: () => true, locale: { value: 'zh-CN' } }),
}))
vi.mock('@/api/channelMonitorV2', () => api)
vi.mock('@/api/groups', () => ({
  default: { getAvailable: vi.fn().mockResolvedValue([]), getUserGroupRates: vi.fn().mockResolvedValue({}) },
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError }) }))

import ChannelStatusV3View from '../ChannelStatusV3View.vue'

const HOUR = 3600 * 1000
const NOW = Date.parse('2026-10-19T11:30:00Z')
const END = Date.parse('2026-10-19T12:00:00Z')

const healthy: MonitorHealth = { overall: 'healthy', error_rate: 'healthy', ttft: 'healthy', minimum_sample: 20 }
function metric(errorRate = 0.02): MonitorMetric {
  return {
    success_requests: 0, error_requests: 0, request_count: 0, token_count: 0, rpm: 0, tpm: 0,
    error_rate: errorRate, cache_rate: 0.4, cache_rate_known: true, cache_rate_numerator: 0, cache_rate_denominator: 0,
    ttft: { sample_count: 0, p50_ms: 800, p95_ms: 1200, avg_ms: 900 },
    duration: { sample_count: 0, p50_ms: 3000, p95_ms: 5000, avg_ms: 3500 },
  }
}
const coverage: MonitorCoverage = {
  requested_start: new Date(END - 24 * HOUR).toISOString(),
  requested_end: new Date(END).toISOString(),
  coverage_start: new Date(END - 24 * HOUR).toISOString(),
  data_through: new Date(NOW).toISOString(),
  computed_at: new Date(NOW).toISOString(),
  aggregation_lag_seconds: 0,
  coverage_complete: true,
  bucket_seconds: 3600,
}
function snapshot(): MonitorSnapshot {
  return {
    config: { refresh_interval_seconds: 60 } as MonitorSnapshot['config'],
    coverage,
    metrics: metric(),
    health: healthy,
    trend: [],
  }
}
function matrix(): MonitorMatrixResponse {
  return {
    coverage,
    group_by: 'platform_group',
    items: [1, 2, 3].map(id => ({
      platform: id === 3 ? 'anthropic' : 'openai',
      group_id: id,
      group_name: `group-${id}`,
      metrics: metric(),
      health: healthy,
      // Active only in the last three hours of the 24h window.
      buckets: [3, 2, 1].map(ago => ({ bucket_start: new Date(END - ago * HOUR).toISOString(), metrics: metric(), health: healthy })),
    })),
  }
}

const updates: Record<string, number> = {}
const countUpdates = {
  updated(this: ComponentPublicInstance) {
    const name = (this.$.type as { __name?: string }).__name
    if (name) updates[name] = (updates[name] ?? 0) + 1
  },
}

let hidden = false
let wrapper: ReturnType<typeof mountView> | undefined

function mountView() {
  return mount(ChannelStatusV3View, {
    attachTo: document.body,
    global: {
      mixins: [countUpdates],
      stubs: { AppLayout: { template: '<div><slot /></div>' }, ProviderIcon: true },
    },
  })
}

const countdownText = () => wrapper?.find('[data-testid="channel-status-countdown"]').text()

describe('ChannelStatusV3View', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(NOW)
    hidden = false
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
    for (const key of Object.keys(updates)) delete updates[key]
    api.getSnapshot.mockReset().mockImplementation(async () => snapshot())
    api.getMatrix.mockReset().mockImplementation(async () => matrix())
    api.getDimensions.mockReset().mockResolvedValue({ platforms: [{ value: 'openai', label: 'openai', request_count: 0 }], groups: [], models: [] })
    showError.mockReset()
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    delete (document as { hidden?: boolean }).hidden
    vi.useRealTimers()
    document.body.innerHTML = ''
  })

  it('ticks the countdown without re-rendering the page, cards or timelines', async () => {
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.findAll('.channel-signal-card')).toHaveLength(3)
    expect(countdownText()).toBe('60s 后刷新')

    const before = { ...updates }
    for (let second = 0; second < 5; second += 1) {
      vi.advanceTimersByTime(1000)
      await nextTick()
    }
    expect(countdownText()).toBe('55s 后刷新')
    expect(updates.ChannelMonitorV3Countdown ?? 0).toBeGreaterThan(before.ChannelMonitorV3Countdown ?? 0)
    expect(updates.ChannelMonitorV3Card ?? 0).toBe(before.ChannelMonitorV3Card ?? 0)
    expect(updates.ChannelMonitorV3Timeline ?? 0).toBe(before.ChannelMonitorV3Timeline ?? 0)
    expect(updates.ChannelStatusV3View ?? 0).toBe(before.ChannelStatusV3View ?? 0)
  })

  it('places each card history on the selected range and shares one tooltip for the board', async () => {
    wrapper = mountView()
    await flushPromises()

    const card = wrapper.findAll('.channel-signal-card')[0]
    const fills = card.findAll('.v3-bar-fill')
    expect(fills).toHaveLength(24)
    expect(fills.slice(0, 21).every(fill => fill.classes().includes('is-empty'))).toBe(true)
    expect(fills.slice(21).every(fill => fill.classes().includes('is-healthy'))).toBe(true)

    // One Teleport for the whole page, not one per bar/card.
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_COMMENT)
    let teleports = 0
    while (walker.nextNode()) if (walker.currentNode.nodeValue === 'teleport start') teleports++
    expect(teleports).toBe(1)

    await card.findAll('.v3-bar')[23].trigger('mouseover')
    const tooltips = document.body.querySelectorAll('[role="tooltip"]')
    expect(tooltips).toHaveLength(1)
    expect(tooltips[0].textContent).toContain('98.0%')
  })

  it('retries a failed first load and keeps refreshing afterwards', async () => {
    api.getMatrix.mockRejectedValueOnce(new Error('boom'))
    wrapper = mountView()
    await flushPromises()
    expect(showError).toHaveBeenCalledTimes(1)
    expect(wrapper.findAll('.channel-signal-card')).toHaveLength(0)
    expect(countdownText()).toBe('30s 后刷新')

    vi.advanceTimersByTime(30_000)
    await flushPromises()
    expect(api.getMatrix).toHaveBeenCalledTimes(2)
    expect(wrapper.findAll('.channel-signal-card')).toHaveLength(3)
    expect(countdownText()).toBe('60s 后刷新')

    vi.advanceTimersByTime(60_000)
    await flushPromises()
    expect(api.getMatrix).toHaveBeenCalledTimes(3)
  })

  it('defers a refresh that falls due while hidden and keeps the countdown on the same deadline', async () => {
    wrapper = mountView()
    await flushPromises()
    hidden = true
    vi.advanceTimersByTime(90_000)
    await flushPromises()
    expect(api.getMatrix).toHaveBeenCalledTimes(1)

    hidden = false
    vi.advanceTimersByTime(1000)
    await nextTick()
    // Counted against the same deadline while hidden, so it does not restart at 60s.
    expect(countdownText()).toBe('0s 后刷新')

    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(api.getMatrix).toHaveBeenCalledTimes(2)
    expect(countdownText()).toBe('60s 后刷新')
  })

  it("shows '-' in the summary when the selected range has no traffic", async () => {
    api.getSnapshot.mockImplementation(async () => ({
      ...snapshot(),
      metrics: { ...metric(0), cache_rate_known: false, ttft: { sample_count: 0, p50_ms: null, p95_ms: null, avg_ms: null }, duration: { sample_count: 0, p50_ms: null, p95_ms: null, avg_ms: null } },
      health: { ...healthy, overall: 'unknown', error_rate: 'unknown' },
    }))
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('成功率 - · 缓存率 —')
  })
})
