import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import AccountCardGrid from '../AccountCardGrid.vue'
import Select from '@/components/common/Select.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const accounts = [
  { id: 1, name: 'alpha' },
  { id: 2, name: 'beta' }
]

const sortColumns = [
  { key: 'name', label: 'Name', sortable: true },
  { key: 'priority', label: 'Priority', sortable: true }
]

function mountGrid(props: Record<string, unknown> = {}) {
  return mount(AccountCardGrid, {
    props: { accounts, sortColumns, sortKey: 'name', sortOrder: 'asc', ...props },
    slots: {
      card: `<template #card="{ account }"><div data-test="card">{{ account.name }}</div></template>`
    },
    global: { stubs: { Icon: true, Teleport: true } }
  })
}

describe('AccountCardGrid', () => {
  it('renders one card per account through the card slot', () => {
    const wrapper = mountGrid()
    expect(wrapper.findAll('[data-test="card"]').map((card) => card.text())).toEqual(['alpha', 'beta'])
  })

  it('shows skeletons while the first page loads and the empty state afterwards', async () => {
    const wrapper = mountGrid({ accounts: [], loading: true })
    expect(wrapper.findAll('[data-testid="account-cards-skeleton"]').length).toBeGreaterThan(0)
    expect(wrapper.get<HTMLInputElement>('[data-testid="account-cards-select-page"]').element.disabled).toBe(true)

    await wrapper.setProps({ loading: false })
    expect(wrapper.find('[data-testid="account-cards-skeleton"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="account-cards-empty"]').exists()).toBe(true)
  })

  it('keeps the current cards dimmed during a reload', () => {
    const wrapper = mountGrid({ loading: true })
    expect(wrapper.findAll('[data-test="card"]')).toHaveLength(2)
    expect(wrapper.get('[data-testid="account-cards"]').attributes('aria-busy')).toBe('true')
  })

  it('selects or clears the page and reflects a partial selection', async () => {
    const wrapper = mountGrid({ selectedIds: [2, 99] })
    const selectPage = wrapper.get<HTMLInputElement>('[data-testid="account-cards-select-page"]')
    expect(selectPage.element.checked).toBe(false)
    expect(selectPage.element.indeterminate).toBe(true)

    await selectPage.setValue(true)
    expect(wrapper.emitted('toggle-select-page')?.[0]).toEqual([true])

    await wrapper.setProps({ selectedIds: [1, 2] })
    expect(selectPage.element.checked).toBe(true)
    expect(selectPage.element.indeterminate).toBe(false)

    await selectPage.setValue(false)
    expect(wrapper.emitted('toggle-select-page')?.[1]).toEqual([false])
  })

  it('sorts by a new field in the current direction and flips the direction', async () => {
    const wrapper = mountGrid({ sortOrder: 'desc' })
    const select = wrapper.findComponent(Select)
    expect(select.props('options')).toEqual([
      { value: 'name', label: 'Name' },
      { value: 'priority', label: 'Priority' }
    ])

    select.vm.$emit('update:modelValue', 'name')
    select.vm.$emit('update:modelValue', 'priority')
    expect(wrapper.emitted('sort')).toEqual([['priority', 'desc']])

    await wrapper.get('[data-testid="account-cards-sort-order"]').trigger('click')
    expect(wrapper.emitted('sort')?.[1]).toEqual(['name', 'asc'])
  })
})
