import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import TokenUsageTrend from '../TokenUsageTrend.vue'

const messages: Record<string, string> = {
  'admin.dashboard.noDataAvailable': 'No data available',
  'admin.dashboard.trend.metricTokens': 'Tokens',
  'admin.dashboard.trend.metricRequests': 'Requests',
  'admin.dashboard.trend.metricCost': 'Cost',
  'admin.dashboard.trend.viewTable': 'Table',
  'admin.dashboard.trend.viewChart': 'Chart',
  'admin.dashboard.trend.viewBar': 'Bar',
  'admin.dashboard.trend.viewLine': 'Line',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

vi.mock('vue-chartjs', () => ({
  Bar: {
    props: ['data', 'options'],
    template: '<div class="bar-chart"><span class="chart-data">{{ JSON.stringify(data) }}</span><span class="chart-scales">{{ Object.keys(options.scales).join(",") }}</span><span class="chart-stacked">{{ String(options.scales.y.stacked) }}</span></div>',
  },
  Line: {
    props: ['data', 'options'],
    template: '<div class="line-chart"><span class="chart-data">{{ JSON.stringify(data) }}</span><span class="chart-scales">{{ Object.keys(options.scales).join(",") }}</span></div>',
  },
}))

const point = (overrides: Record<string, unknown> = {}) => ({
  date: '2026-05-08',
  requests: 1,
  input_tokens: 500,
  output_tokens: 100,
  cache_creation_tokens: 0,
  cache_read_tokens: 1500,
  total_tokens: 2100,
  cost: 0.01,
  actual_cost: 0.005,
  ...overrides,
})

const mountTrend = (trendData: Array<ReturnType<typeof point>>) =>
  mount(TokenUsageTrend, { props: { trendData } })

describe('TokenUsageTrend', () => {
  beforeEach(() => {
    window.localStorage.clear()
  })

  it('calculates cache hit rate against all prompt tokens', () => {
    // Hit rate = 1500 / (500 + 1500 + 0) = 75%
    const wrapper = mountTrend([point()])
    expect(wrapper.text()).toContain('75.0%')
  })

  it('returns 0 hit rate when all prompt tokens are zero', () => {
    const wrapper = mountTrend([
      point({ requests: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, total_tokens: 0, cost: 0, actual_cost: 0 }),
    ])
    expect(wrapper.text()).toContain('0.0%')
  })

  it('includes cache_creation_tokens in denominator for Anthropic models', () => {
    // Hit rate = 500 / (200 + 500 + 300) = 50%
    const wrapper = mountTrend([
      point({ input_tokens: 200, output_tokens: 50, cache_creation_tokens: 300, cache_read_tokens: 500, total_tokens: 1050 }),
    ])
    expect(wrapper.text()).toContain('50.0%')
  })

  it('renders token composition as stacked bars on a single y axis', () => {
    const wrapper = mountTrend([point(), point({ date: '2026-05-09' })])

    const data = JSON.parse(wrapper.find('.bar-chart .chart-data').text())
    // 通常占比最大的缓存读取压在最底层并使用第 1 色
    expect(data.datasets.map((ds: { label: string }) => ds.label)).toEqual([
      'admin.dashboard.trend.cacheRead',
      'admin.dashboard.trend.input',
      'admin.dashboard.trend.output',
      'admin.dashboard.trend.cacheCreation',
    ])
    expect(data.datasets[0].data).toEqual([1500, 1500])
    expect(data.datasets[1].data).toEqual([500, 500])
    // 不再使用双 Y 轴：缓存命中率改在标题区展示
    expect(wrapper.find('.bar-chart .chart-scales').text()).toBe('x,y')
  })

  it('switches to request and cost trends', async () => {
    const wrapper = mountTrend([point({ requests: 3 }), point({ date: '2026-05-09', requests: 7, actual_cost: 0.2, cost: 0.4 })])

    const requestsTab = wrapper.findAll('button').find((b) => b.text() === 'Requests')
    await requestsTab!.trigger('click')
    let data = JSON.parse(wrapper.find('.line-chart .chart-data').text())
    expect(data.datasets).toHaveLength(1)
    expect(data.datasets[0].data).toEqual([3, 7])

    const costTab = wrapper.findAll('button').find((b) => b.text() === 'Cost')
    await costTab!.trigger('click')
    data = JSON.parse(wrapper.find('.line-chart .chart-data').text())
    expect(data.datasets.map((ds: { data: number[] }) => ds.data)).toEqual([
      [0.005, 0.2],
      [0.01, 0.4],
    ])
    expect(wrapper.find('.line-chart .chart-scales').text()).toBe('x,y')
  })

  it('offers a data table view as the accessible equivalent of the chart', async () => {
    const wrapper = mountTrend([point(), point({ date: '2026-05-09' })])

    await wrapper.get('button[aria-label="Table"]').trigger('click')

    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('2026-05-08')
    expect(rows[0].text()).toContain('500')
  })

  it('offers a line chart for token composition without stacking', async () => {
    const wrapper = mountTrend([point(), point({ date: '2026-05-09' })])
    expect(wrapper.find('.bar-chart').exists()).toBe(true)

    await wrapper.get('button[aria-label="Line"]').trigger('click')

    expect(wrapper.find('.bar-chart').exists()).toBe(false)
    const data = JSON.parse(wrapper.get('.line-chart .chart-data').text())
    expect(data.datasets).toHaveLength(4)
    expect(data.datasets[0].data).toEqual([1500, 1500])
    expect(wrapper.get('.line-chart .chart-scales').text()).toBe('x,y')
  })

  it('keeps the chosen chart type across metrics and reloads', async () => {
    const wrapper = mountTrend([point({ requests: 3, actual_cost: 0.2 }), point({ date: '2026-05-09', requests: 7, actual_cost: 0.3 })])
    await wrapper.findAll('button').find((b) => b.text() === 'Requests')!.trigger('click')
    // Requests default to a line until a chart type is chosen
    expect(wrapper.find('.line-chart').exists()).toBe(true)

    await wrapper.get('button[aria-label="Bar"]').trigger('click')
    let data = JSON.parse(wrapper.get('.bar-chart .chart-data').text())
    expect(data.datasets[0].data).toEqual([3, 7])
    expect(window.localStorage.getItem('sub2api.usageTrend.chartType')).toBe('bar')

    // Cost also defaults to a line; the explicit bar choice carries over
    await wrapper.findAll('button').find((b) => b.text() === 'Cost')!.trigger('click')
    expect(wrapper.find('.bar-chart').exists()).toBe(true)

    await wrapper.get('button[aria-label="Line"]').trigger('click')
    const reloaded = mountTrend([point(), point({ date: '2026-05-09' })])
    // Tokens default to bars, but the saved line preference applies after a reload
    expect(reloaded.find('.line-chart').exists()).toBe(true)
    data = JSON.parse(reloaded.get('.line-chart .chart-data').text())
    expect(data.datasets).toHaveLength(4)
  })

  it('draws cost bars side by side instead of stacking them', async () => {
    const wrapper = mountTrend([point({ actual_cost: 0.2, cost: 0.4 })])
    await wrapper.findAll('button').find((b) => b.text() === 'Cost')!.trigger('click')
    await wrapper.get('button[aria-label="Bar"]').trigger('click')

    expect(wrapper.get('.bar-chart .chart-stacked').text()).toBe('false')
    const data = JSON.parse(wrapper.get('.bar-chart .chart-data').text())
    expect(data.datasets.map((ds: { data: number[] }) => ds.data)).toEqual([[0.2], [0.4]])

    // Token composition still stacks in bar mode
    await wrapper.findAll('button').find((b) => b.text() === 'Tokens')!.trigger('click')
    expect(wrapper.get('.bar-chart .chart-stacked').text()).toBe('true')
  })
})
