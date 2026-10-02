import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PlazaModelCatalog from '../PlazaModelCatalog.vue'
import ModelPlazaContent from '../ModelPlazaContent.vue'
import type { ModelPlazaCatalogModel, ModelPlazaResponse } from '@/api/modelPlaza'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${JSON.stringify(params)}` : key
    })
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ cachedPublicSettings: null })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isAuthenticated: true })
}))

const stubs = { Icon: true, PlatformIcon: true }

function model(overrides: Partial<ModelPlazaCatalogModel>): ModelPlazaCatalogModel {
  return {
    name: 'model',
    platform: 'anthropic',
    official_pricing: null,
    groups: [],
    ungrouped: false,
    ...overrides
  }
}

const models: ModelPlazaCatalogModel[] = [
  model({
    name: 'claude-sonnet-4-6',
    official_pricing: {
      input_price: 3e-6,
      output_price: 1.5e-5,
      cache_write_price: 3.75e-6,
      cache_read_price: 3e-7,
      intervals: [
        { min_tokens: 0, max_tokens: 200000, input_price: 3e-6, output_price: 1.5e-5, cache_write_price: null, cache_read_price: null, per_request_price: null },
        { min_tokens: 200000, max_tokens: null, input_price: 6e-6, output_price: 2.25e-5, cache_write_price: null, cache_read_price: null, per_request_price: null }
      ]
    },
    groups: [{ id: 1, name: 'claude-pool', rate_multiplier: 0.8, user_rate_multiplier: 0.5 }],
    ungrouped: true
  }),
  model({
    name: 'claude-opus-4-8',
    official_pricing: { input_price: 1.5e-5, output_price: 7.5e-5, cache_write_price: null, cache_read_price: null }
  }),
  model({ name: 'claude-unpriced' }),
  model({ name: 'gpt-5', platform: 'openai', official_pricing: { input_price: 1.25e-6, output_price: 1e-5, cache_write_price: null, cache_read_price: 1.25e-7 } })
]

const rowNames = (wrapper: ReturnType<typeof mount>) =>
  wrapper.findAll('[data-testid="plaza-catalog-row"]').map((row) => row.find('td .font-mono').text())

describe('PlazaModelCatalog', () => {
  it('shows per-million API prices and the groups that serve each model', () => {
    const wrapper = mount(PlazaModelCatalog, { props: { models }, global: { stubs } })

    // 平台内有价的按输出价降序，无价的沉底
    expect(rowNames(wrapper)).toEqual(['claude-opus-4-8', 'claude-sonnet-4-6', 'claude-unpriced', 'gpt-5'])

    const sonnet = wrapper.findAll('[data-testid="plaza-catalog-row"]')[1]!
    const cells = sonnet.findAll('td.price-cell').map((cell) => cell.text())
    expect(cells).toEqual(['$3.00', '$15.00', '$3.75', '$0.30'])
    // 生效倍率优先取用户专属倍率；未分组 Key 按基础价
    expect(sonnet.text()).toContain('claude-pool')
    expect(sonnet.text()).toContain('×0.5')
    expect(sonnet.text()).toContain('modelPlaza.catalog.ungrouped')
    expect(sonnet.find('[data-testid="plaza-catalog-tiered"]').attributes('title')).toContain('$22.50')

    const unpriced = wrapper.findAll('[data-testid="plaza-catalog-row"]')[2]!
    expect(unpriced.findAll('td.price-cell').map((cell) => cell.text())).toEqual(['-', '-', '-', '-'])
  })

  it('filters by platform and model name', async () => {
    const wrapper = mount(PlazaModelCatalog, { props: { models }, global: { stubs } })

    await wrapper.get('[data-testid="plaza-catalog-platform-openai"]').trigger('click')
    expect(rowNames(wrapper)).toEqual(['gpt-5'])

    await wrapper.get('[data-testid="plaza-catalog-platform-all"]').trigger('click')
    await wrapper.get('[data-testid="plaza-catalog-search"]').setValue('OPUS')
    expect(rowNames(wrapper)).toEqual(['claude-opus-4-8'])

    await wrapper.get('[data-testid="plaza-catalog-search"]').setValue('nothing-matches')
    expect(wrapper.find('[data-testid="plaza-catalog-row"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('modelPlaza.noSearchResult')
  })
})

describe('ModelPlazaContent views', () => {
  const response: ModelPlazaResponse = { description: '', groups: [], models }

  it('opens on the per-model price list and switches to the group view', async () => {
    const wrapper = mount(ModelPlazaContent, {
      props: { response, loading: false },
      global: { stubs: { ...stubs, PlazaFilterBar: true, PlazaGroupSection: true } }
    })

    expect(wrapper.find('[data-testid="plaza-catalog"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="plaza-view-models"]').text()).toContain('4')

    await wrapper.get('[data-testid="plaza-view-groups"]').trigger('click')
    expect(wrapper.find('[data-testid="plaza-catalog"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('modelPlaza.empty')
  })

  it('treats a response without models as an empty catalog', () => {
    const wrapper = mount(ModelPlazaContent, {
      props: { response: { description: '', groups: [] }, loading: false },
      global: { stubs: { ...stubs, PlazaFilterBar: true, PlazaGroupSection: true } }
    })

    expect(wrapper.text()).toContain('modelPlaza.catalog.empty')
  })
})
