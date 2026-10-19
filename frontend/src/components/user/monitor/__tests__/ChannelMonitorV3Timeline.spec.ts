import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { reactive, type ComponentPublicInstance } from 'vue'
import type { MonitorHealth, MonitorMatrixBucket, MonitorMatrixRow, MonitorMetric } from '@/api/channelMonitorV2'
import { buildChannelStatusAxis, labelChannelStatusAxis } from '@/features/channel-monitor-v2/channelStatusLayout'

const messages: Record<string, string> = {
  'channelMonitorV2.matrix.noTrafficAt': '{time} · 无流量',
  'channelMonitorV3.timelineTooltip': '{time} · 可用率 {availability} · 缓存率 {cache} · 首 Token {ttft}',
  'monitorCommon.history60pts': '近 {n} 次记录',
}
const t = (key: string, params?: Record<string, unknown>) =>
  (messages[key] ?? key).replace(/\{(\w+)\}/g, (_, name) => String(params?.[name] ?? ''))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t, te: () => true, locale: { value: 'zh-CN' } }),
}))

import ChannelMonitorV3Timeline from '../ChannelMonitorV3Timeline.vue'
import ChannelMonitorV3Card from '../ChannelMonitorV3Card.vue'
import { createV3TooltipState, V3_TOOLTIP_HOST } from '../v3TimelineTooltip'

const HOUR = 3600 * 1000
const END = Date.parse('2026-10-19T12:00:00Z')

function metric(overrides: Partial<MonitorMetric> = {}): MonitorMetric {
  // Shape of a user (redacted) payload: absolute counts are zeroed by the backend.
  return {
    success_requests: 0, error_requests: 0, request_count: 0, token_count: 0, rpm: 0, tpm: 0,
    error_rate: 0.05, cache_rate: 0.4, cache_rate_known: true, cache_rate_numerator: 0, cache_rate_denominator: 0,
    ttft: { sample_count: 0, p50_ms: 800, p95_ms: 1200, avg_ms: 900 },
    duration: { sample_count: 0, p50_ms: 3000, p95_ms: 5000, avg_ms: 3500 },
    ...overrides,
  }
}
const healthy: MonitorHealth = { overall: 'healthy', error_rate: 'healthy', ttft: 'healthy', minimum_sample: 20 }
const unknown: MonitorHealth = { overall: 'unknown', error_rate: 'unknown', ttft: 'unknown', minimum_sample: 20 }
/** No traffic at all: error_rate 0 used to render as a green "100%". */
const emptyMetric = () => metric({
  error_rate: 0, cache_rate: 0, cache_rate_known: false,
  ttft: { sample_count: 0, p50_ms: null, p95_ms: null, avg_ms: null },
  duration: { sample_count: 0, p50_ms: null, p95_ms: null, avg_ms: null },
})

function bucketAt(start: number, overrides: Partial<MonitorMatrixBucket> = {}): MonitorMatrixBucket {
  return { bucket_start: new Date(start).toISOString(), metrics: metric(), health: healthy, ...overrides }
}

/** Same axis the page builds from matrix.coverage for a 24h range (hourly buckets). */
function axis24h() {
  const starts = buildChannelStatusAxis({
    requested_start: new Date(END - 24 * HOUR).toISOString(),
    requested_end: new Date(END).toISOString(),
    data_through: new Date(END - HOUR / 2).toISOString(),
    bucket_seconds: 3600,
  })
  return labelChannelStatusAxis(starts, 'zh-CN', time => t('channelMonitorV2.matrix.noTrafficAt', { time }))
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('ChannelMonitorV3Timeline', () => {
  it('places sparse buckets on the coverage axis by bucket_start, keeping gaps in place', () => {
    const axis = axis24h()
    expect(axis).toHaveLength(24)
    // Only active in the last 3 hours, plus one isolated bucket 20h ago, delivered unsorted.
    const buckets = [END - HOUR, END - 3 * HOUR, END - 20 * HOUR, END - 2 * HOUR].map(start => bucketAt(start))
    const wrapper = mount(ChannelMonitorV3Timeline, { props: { buckets, axis } })

    const bars = wrapper.findAll('.v3-bar')
    expect(bars).toHaveLength(24)
    const filled = bars.map(bar => !bar.find('.v3-bar-fill').classes().includes('is-empty'))
    const expected = Array.from({ length: 24 }, (_, index) => [4, 21, 22, 23].includes(index))
    expect(filled).toEqual(expected)
    // Empty slots read as "no data" for assistive tech instead of collapsing away.
    expect(bars[0].attributes('aria-label')).toMatch(/· 无流量$/)
    expect(bars[23].attributes('aria-label')).toBe(axis[23].time)
    expect(wrapper.text()).toContain('近 24 次记录')
  })

  it('ignores buckets outside the axis instead of shifting the window', () => {
    const axis = axis24h()
    const wrapper = mount(ChannelMonitorV3Timeline, {
      props: { axis, buckets: [bucketAt(END - 30 * HOUR), bucketAt(END), bucketAt(END - HOUR)] },
    })
    const fills = wrapper.findAll('.v3-bar-fill')
    expect(fills).toHaveLength(24)
    expect(fills.filter(fill => !fill.classes().includes('is-empty'))).toHaveLength(1)
    expect(fills[23].classes()).toContain('is-healthy')
  })

  it('renders bars without per-bar tooltip subtrees and shows one shared tooltip on demand', async () => {
    const axis = axis24h()
    const host = { id: 'shared-tooltip', state: reactive(createV3TooltipState()) }
    let timelineUpdates = 0
    const wrapper = mount(ChannelMonitorV3Timeline, {
      attachTo: document.body,
      props: { axis, buckets: [bucketAt(END - HOUR), bucketAt(END - 5 * HOUR, { metrics: emptyMetric(), health: unknown })] },
      global: {
        provide: { [V3_TOOLTIP_HOST as symbol]: host },
        mixins: [{
          updated(this: ComponentPublicInstance) {
            if ((this.$.type as { __name?: string }).__name === 'ChannelMonitorV3Timeline') timelineUpdates++
          },
        }],
      },
    })

    // Each bar is exactly a hit column plus its fill: no Teleport/Transition anchors, no tooltip nodes.
    const barsEl = wrapper.find('.v3-timeline-bars').element
    expect(barsEl.querySelectorAll('*')).toHaveLength(24 * 2)
    expect(wrapper.html()).not.toContain('teleport')
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull()
    const bars = wrapper.findAll('.v3-bar')
    expect(bars.every(bar => bar.attributes('aria-describedby') === 'shared-tooltip')).toBe(true)
    // Bars carry no inline style and no per-bar positioning hooks.
    expect(bars.every(bar => bar.attributes('style') === undefined)).toBe(true)

    await bars[23].find('.v3-bar-fill').trigger('mouseover')
    expect(host.state.visible).toBe(true)
    expect(host.state.text).toBe(`${axis[23].time} · 可用率 95.0% · 缓存率 40.0% · 首 Token 800ms`)

    // A bucket that exists but has no samples must not claim 100% availability.
    await bars[19].trigger('mouseover')
    expect(host.state.text).toBe(`${axis[19].time} · 可用率 - · 缓存率 — · 首 Token -`)
    expect(bars[19].find('.v3-bar-fill').classes()).toContain('is-unknown')

    await bars[0].trigger('mouseover')
    expect(host.state.text).toBe(axis[0].emptyLabel)

    await wrapper.find('.v3-timeline-bars').trigger('mouseleave')
    expect(host.state.visible).toBe(false)
    // Hover state lives in the shared tooltip only: the timeline never re-rendered.
    expect(timelineUpdates).toBe(0)
    wrapper.unmount()
  })

  it('falls back to its own floating tooltip when no page host is provided', async () => {
    const axis = axis24h()
    const wrapper = mount(ChannelMonitorV3Timeline, { attachTo: document.body, props: { axis, buckets: [bucketAt(END - HOUR)] } })
    await wrapper.findAll('.v3-bar')[23].trigger('focusin')
    const tooltip = document.body.querySelector('[role="tooltip"]')
    expect(tooltip?.textContent).toContain('可用率 95.0%')
    expect(document.body.querySelectorAll('[role="tooltip"]')).toHaveLength(1)
    wrapper.unmount()
  })
})

describe('ChannelMonitorV3Card success rate', () => {
  function row(metrics: MonitorMetric, health: MonitorHealth): MonitorMatrixRow {
    return { platform: 'openai', group_id: 1, group_name: 'g1', metrics, health, buckets: [] }
  }
  const mountCard = (value: MonitorMatrixRow) =>
    mount(ChannelMonitorV3Card, { props: { row: value, axis: axis24h() }, global: { stubs: { ProviderIcon: true } } })

  it("shows a neutral '-' for a group without samples instead of a green 100%", () => {
    const wrapper = mountCard(row(emptyMetric(), unknown))
    const value = wrapper.get('[data-testid="channel-status-success-rate"]')
    expect(value.text()).toBe('-')
    expect(value.classes().some(name => name.startsWith('text-emerald'))).toBe(false)
    expect(wrapper.find('[data-testid="channel-status-sample-badge"]').exists()).toBe(true)
  })

  it('keeps a measured low-sample rate but without the good/bad colour', () => {
    const wrapper = mountCard(row(metric({ error_rate: 0 }), unknown))
    const value = wrapper.get('[data-testid="channel-status-success-rate"]')
    expect(value.text()).toBe('100.0%')
    expect(value.classes().some(name => name.startsWith('text-emerald'))).toBe(false)
  })

  it('colours a rate backed by enough samples', () => {
    const wrapper = mountCard(row(metric({ error_rate: 0.01 }), healthy))
    const value = wrapper.get('[data-testid="channel-status-success-rate"]')
    expect(value.text()).toBe('99.0%')
    expect(value.classes()).toContain('text-emerald-800')
    expect(wrapper.find('[data-testid="channel-status-sample-badge"]').exists()).toBe(false)
  })
})
