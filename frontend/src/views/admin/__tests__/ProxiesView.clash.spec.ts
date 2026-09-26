import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import ProxiesView from '../ProxiesView.vue'
import Select from '@/components/common/Select.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import type { Proxy } from '@/types'

const { list, getAllWithCount, batchDelete, showInfo } = vi.hoisted(() => ({
  list: vi.fn(),
  getAllWithCount: vi.fn(),
  batchDelete: vi.fn(),
  showInfo: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { proxies: { list, getAllWithCount, batchDelete } }
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo })
}))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

const proxy = (id: number, source: Proxy['source']): Proxy => ({
  id,
  name: `proxy-${id}`,
  protocol: 'socks5',
  host: source === 'clash' ? '127.0.0.1' : `p${id}.example`,
  port: 1000 + id,
  username: null,
  status: 'active',
  expires_at: null,
  fallback_mode: 'none',
  expiry_warn_days: 7,
  created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-01T00:00:00Z',
  source
})

const mountView = () => shallowMount(ProxiesView, {
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /></div>' },
      DataTable: {
        props: ['data'],
        template: `<div>
          <div data-test="header"><slot name="header-select" /></div>
          <div v-for="row in data" :key="row.id" :data-row="row.id">
            <slot name="cell-select" :row="row" />
            <slot name="cell-name" :row="row" :value="row.name" />
            <slot name="cell-actions" :row="row" />
          </div>
        </div>`
      }
    }
  }
})

let wrapper: ReturnType<typeof mountView>

beforeEach(() => {
  vi.clearAllMocks()
  getAllWithCount.mockResolvedValue([])
  list.mockResolvedValue({ items: [proxy(1, 'manual'), proxy(2, 'clash')], total: 2, pages: 1 })
  batchDelete.mockResolvedValue({ deleted_ids: [1], skipped: [] })
})

afterEach(() => wrapper?.unmount())

describe('ProxiesView — Clash-managed proxies', () => {
  it('lists manual proxies by default and switches the source filter', async () => {
    wrapper = mountView()
    await flushPromises()
    expect(list.mock.calls[0][2]).toMatchObject({ source: 'manual' })

    const source = wrapper.findAllComponents(Select).find((select) => select.props('placeholder') === 'admin.proxies.sourceFilter')!
    expect(source.props('modelValue')).toBe('manual')
    expect(source.props('options').map((option: { value: string }) => option.value)).toEqual(['manual', 'clash', 'all'])

    source.vm.$emit('update:modelValue', 'clash')
    source.vm.$emit('change', 'clash')
    await flushPromises()
    expect(list).toHaveBeenLastCalledWith(1, expect.any(Number), expect.objectContaining({ source: 'clash' }), expect.anything())
  })

  it('marks Clash rows read-only', async () => {
    wrapper = mountView()
    await flushPromises()

    const manualRow = wrapper.get('[data-row="1"]')
    const clashRow = wrapper.get('[data-row="2"]')
    expect(clashRow.findComponent({ name: 'ClashTag' }).exists()).toBe(true)
    expect(manualRow.findComponent({ name: 'ClashTag' }).exists()).toBe(false)
    expect(clashRow.get('input[type="checkbox"]').attributes('disabled')).toBeDefined()

    const clashButtons = clashRow.findAll('button').filter((button) => ['common.edit', 'common.delete'].includes(button.text()))
    expect(clashButtons).toHaveLength(2)
    for (const button of clashButtons) {
      expect(button.attributes('disabled')).toBeDefined()
      expect(button.attributes('title')).toBe('admin.proxies.clashManagedHint')
    }
    const manualEdit = manualRow.findAll('button').find((button) => button.text() === 'common.edit')!
    expect(manualEdit.attributes('disabled')).toBeUndefined()
  })

  it('excludes Clash rows from select-all and batch delete', async () => {
    wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="header"] input[type="checkbox"]').setValue(true)
    expect((wrapper.get('[data-row="1"] input[type="checkbox"]').element as HTMLInputElement).checked).toBe(true)
    expect((wrapper.get('[data-row="2"] input[type="checkbox"]').element as HTMLInputElement).checked).toBe(false)

    await wrapper.findAll('button').find((button) => button.attributes('title') === 'admin.proxies.batchDeleteAction')!.trigger('click')
    const confirm = wrapper.findAllComponents(ConfirmDialog).find((dialog) => dialog.props('title') === 'admin.proxies.batchDelete')!
    confirm.vm.$emit('confirm')
    await flushPromises()
    expect(batchDelete).toHaveBeenCalledWith([1])
  })
})
