import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageHeatmap from '../UsageHeatmap.vue'
import TodayModelUsageCard from '../TodayModelUsageCard.vue'
import CustomerUsageTable from '../CustomerUsageTable.vue'
import RevenueProfitTrend from '../RevenueProfitTrend.vue'
import TokenUsageTrend from '@/components/charts/TokenUsageTrend.vue'
import { cacheHitRatio, marginRatio } from '@/components/charts/chartTheme'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
      locale: { value: 'en' }
    })
  }
})

vi.mock('vue-chartjs', () => ({
  Bar: { props: ['data', 'options'], template: '<div class="bar-chart" />' },
  Line: {
    props: ['data', 'options'],
    template: '<div class="line-chart"><span class="chart-data">{{ JSON.stringify(data) }}</span><span class="chart-y-max">{{ options.scales.y.max }}</span></div>'
  }
}))

const hourPoint = (date: string, requests: number) => ({
  date,
  requests,
  input_tokens: requests * 10,
  output_tokens: requests,
  cache_creation_tokens: 0,
  cache_read_tokens: requests * 30,
  total_tokens: requests * 41,
  cost: requests * 0.02,
  actual_cost: requests * 0.01
})

describe('ratio helpers', () => {
  it('measures cache hits against every prompt token and margin against revenue', () => {
    expect(cacheHitRatio(100, 100, 200)).toBe(0.5)
    expect(cacheHitRatio(0, 0, 0)).toBeNull()
    expect(marginRatio(10, 6)).toBeCloseTo(0.4)
    expect(marginRatio(10, 15)).toBeCloseTo(-0.5)
    expect(marginRatio(0, 3)).toBeNull()
  })
})

describe('UsageHeatmap', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    // Thursday 2026-10-01 10:30 local time: later hours of that day are still in the future.
    vi.setSystemTime(new Date(2026, 9, 1, 10, 30))
  })
  afterEach(() => vi.useRealTimers())

  const cells = (wrapper: ReturnType<typeof mount>) => wrapper.findAll('.grid > span[style*="background"]')

  it('fills every day of the range with 24 hours and dims hours that have not happened', () => {
    const wrapper = mount(UsageHeatmap, {
      props: {
        points: [hourPoint('2026-09-30 09:00', 4), hourPoint('2026-10-01 09:00', 2)],
        startDate: '2026-09-29',
        endDate: '2026-10-01'
      }
    })
    // 3 days × 24 hours, including the empty 09-29 row
    expect(cells(wrapper)).toHaveLength(72)
    const today = cells(wrapper).slice(48)
    expect(today[9].classes()).not.toContain('opacity-40')
    expect(today[11].classes()).toContain('opacity-40')
    expect(wrapper.text()).toContain('admin.dashboard.heatmap.peak')
    expect(wrapper.text()).toContain('2026-09-30')
  })

  it('averages by weekday without counting future hours as zero', async () => {
    const wrapper = mount(UsageHeatmap, {
      props: {
        // Two Thursdays (09-24 and 10-01) with 4 and 2 requests at 09:00 -> average 3.
        // At 15:00 only the past Thursday has data; today's 15:00 has not happened yet,
        // so its average is 8, not 4.
        points: [
          hourPoint('2026-09-24 09:00', 4),
          hourPoint('2026-10-01 09:00', 2),
          hourPoint('2026-09-24 15:00', 8)
        ],
        startDate: '2026-09-24',
        endDate: '2026-10-01'
      }
    })
    await wrapper.findAll('button').find((b) => b.text() === 'admin.dashboard.heatmap.byWeekday')!.trigger('click')
    await wrapper.findAll('button').find((b) => b.attributes('title') === 'admin.dashboard.trend.viewTable')!.trigger('click')

    const thursday = wrapper.findAll('tbody tr')[3]
    const values = thursday.findAll('td').map((td) => td.text())
    expect(values[9]).toBe('3')
    expect(values[15]).toBe('8')
  })
})

describe('TodayModelUsageCard', () => {
  const model = (name: string, actual: number) => ({
    model: name,
    requests: 10,
    input_tokens: 100,
    output_tokens: 50,
    cache_creation_tokens: 0,
    cache_read_tokens: 300,
    total_tokens: 450,
    cost: actual * 2,
    actual_cost: actual,
    account_cost: actual / 2
  })

  it('folds the tail into an Other row and shows cache hit rate and margin', () => {
    const models = Array.from({ length: 12 }, (_, i) => model(`model-${i}`, 12 - i))
    const wrapper = mount(TodayModelUsageCard, { props: { models, limit: 10 } })
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(11)
    expect(rows[0].text()).toContain('model-0')
    expect(rows[10].text()).toContain('admin.dashboard.todayModels.other:{"count":2}')
    // cache hit 300 / (100 + 0 + 300) = 75%, margin (x - x/2) / x = 50%
    expect(rows[0].text()).toContain('75.0%')
    expect(rows[0].text()).toContain('50.0%')
  })
})

describe('CustomerUsageTable', () => {
  it('computes profit, margin and share, and hides hit rate without a cache split', async () => {
    const wrapper = mount(CustomerUsageTable, {
      props: {
        sortBy: 'actual_cost',
        totalRevenue: 20,
        users: [
          { user_id: 1, email: 'a@x.test', requests: 3, input_tokens: 100, output_tokens: 10, cache_tokens: 300, total_tokens: 410, cost: 12, actual_cost: 10, account_cost: 4, cache_creation_tokens: 100, cache_read_tokens: 200 },
          { user_id: 2, email: 'b@x.test', requests: 1, input_tokens: 10, output_tokens: 1, cache_tokens: 0, total_tokens: 11, cost: 1, actual_cost: 1, account_cost: 2 }
        ]
      }
    })
    const [first, second] = wrapper.findAll('tbody tr')
    // share 10/20, hit 200/(100+100+200), profit 6, margin 60%
    expect(first.text()).toContain('50.0%')
    expect(first.text()).toContain('$6.00')
    expect(first.text()).toContain('60.0%')
    expect(second.text()).toContain('—')
    expect(second.text()).toContain('-100.0%')

    await first.trigger('click')
    expect(wrapper.emitted('select')?.[0]).toEqual([1])
  })
})

describe('RevenueProfitTrend', () => {
  it('plots revenue, account cost and gross profit on one dollar axis', () => {
    const wrapper = mount(RevenueProfitTrend, {
      props: {
        points: [
          { date: '2026-10-01', requests: 1, cost: 3, actual_cost: 2, account_cost: 1.5 },
          { date: '2026-10-02', requests: 1, cost: 3, actual_cost: 1, account_cost: 1.5 }
        ]
      }
    })
    const data = JSON.parse(wrapper.get('.chart-data').text())
    expect(data.datasets.map((d: { data: number[] }) => d.data)).toEqual([
      [2, 1],
      [1.5, 1.5],
      [0.5, -0.5]
    ])
    // overall margin (3 - 3) / 3
    expect(wrapper.text()).toContain('0.0%')
  })
})

describe('TokenUsageTrend cache rate metric', () => {
  it('is opt-in and plots the per-bucket hit rate on a 0-100% axis', async () => {
    const points = [hourPoint('2026-10-01 09:00', 1), { ...hourPoint('2026-10-01 10:00', 0) }]
    const plain = mount(TokenUsageTrend, { props: { trendData: points } })
    expect(plain.text()).not.toContain('admin.dashboard.trend.metricCacheRate')

    const wrapper = mount(TokenUsageTrend, { props: { trendData: points, enableCacheRate: true } })
    await wrapper.findAll('button').find((b) => b.text() === 'admin.dashboard.trend.metricCacheRate')!.trigger('click')
    const data = JSON.parse(wrapper.get('.chart-data').text())
    // 30 / (10 + 0 + 30) = 75%; the empty hour is a gap, not 0%
    expect(data.datasets[0].data).toEqual([0.75, null])
    expect(wrapper.get('.chart-y-max').text()).toBe('1')
  })
})
