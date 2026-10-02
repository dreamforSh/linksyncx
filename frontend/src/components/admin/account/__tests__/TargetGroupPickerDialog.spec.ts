import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import TargetGroupPickerDialog from '../TargetGroupPickerDialog.vue'

const authState = vi.hoisted(() => ({ isSimpleMode: false }))

vi.mock('@/stores', () => ({
  useAuthStore: () => authState
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const BaseDialogStub = defineComponent({
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const group = (overrides: Partial<AdminGroup>): AdminGroup =>
  ({ status: 'active', kind: 'channel', description: null, account_count: 0, ...overrides }) as AdminGroup

const claudePool = group({ id: 1, name: 'claude-pool', platform: 'anthropic', account_count: 3 })
const routeAll = group({ id: 2, name: 'route-all', platform: 'composite' })
const acme = group({ id: 3, name: 'acme', platform: 'anthropic', kind: 'managed', category: 'enterprise' })

function mountPicker(groups: AdminGroup[], props: Record<string, unknown> = {}) {
  return mount(TargetGroupPickerDialog, {
    props: { show: true, groups, ...props },
    global: {
      stubs: { BaseDialog: BaseDialogStub, Icon: true, PlatformIcon: true }
    }
  })
}

const optionIds = (wrapper: ReturnType<typeof mountPicker>) =>
  wrapper.findAll('input[type="radio"]').map(input => Number((input.element as HTMLInputElement).value))

describe('TargetGroupPickerDialog', () => {
  beforeEach(() => {
    authState.isSimpleMode = false
  })

  it('lists channel groups first and keeps next disabled until a group is chosen', async () => {
    const wrapper = mountPicker([claudePool, routeAll, acme])

    expect(optionIds(wrapper)).toEqual([1, 2])
    expect(wrapper.get('[data-testid="target-group-next"]').attributes('disabled')).toBeDefined()

    await wrapper.get('[data-testid="target-group-option-2"] input').setValue(true)
    await wrapper.get('[data-testid="target-group-next"]').trigger('click')

    expect(wrapper.emitted('select')?.[0]).toEqual([routeAll])
  })

  it('switches to managed groups and preselects the only candidate', async () => {
    const wrapper = mountPicker([claudePool, routeAll, acme])

    await wrapper.get('[data-testid="target-group-tab-managed"]').trigger('click')

    expect(optionIds(wrapper)).toEqual([3])
    expect(wrapper.text()).toContain('admin.groups.groupKind.enterprise')
    await wrapper.get('[data-testid="target-group-next"]').trigger('click')
    expect(wrapper.emitted('select')?.[0]).toEqual([acme])
  })

  it('opens on the tab of the initial group with it selected', () => {
    const wrapper = mountPicker([claudePool, routeAll, acme], { initialGroupId: 3 })

    expect(wrapper.get('[data-testid="target-group-tab-managed"]').attributes('aria-selected')).toBe('true')
    expect((wrapper.get('[data-testid="target-group-option-3"] input').element as HTMLInputElement).checked).toBe(true)
  })

  it('offers to create a group when the tab is empty', async () => {
    const wrapper = mountPicker([claudePool])

    await wrapper.get('[data-testid="target-group-tab-managed"]').trigger('click')

    expect(wrapper.find('[data-testid="target-group-empty"]').exists()).toBe(true)
    await wrapper.get('[data-testid="target-group-empty"] button').trigger('click')
    expect(wrapper.emitted('create-group')?.[0]).toEqual(['managed'])
  })

  it('lets the admin continue without a group', async () => {
    const wrapper = mountPicker([])

    await wrapper.get('[data-testid="target-group-skip"]').trigger('click')

    expect(wrapper.emitted('skip')).toHaveLength(1)
    expect(wrapper.emitted('select')).toBeUndefined()
  })

  it('filters long lists by name', async () => {
    const many = Array.from({ length: 8 }, (_, index) =>
      group({ id: 10 + index, name: index === 5 ? 'special-pool' : `pool-${index}`, platform: 'openai' })
    )
    const wrapper = mountPicker(many)

    await wrapper.get('[data-testid="target-group-search"]').setValue('special')

    expect(optionIds(wrapper)).toEqual([15])
  })

  it('hides composite groups in simple mode', () => {
    authState.isSimpleMode = true
    const wrapper = mountPicker([claudePool, routeAll])

    expect(optionIds(wrapper)).toEqual([1])
    // 只剩一个候选时直接选中
    expect(wrapper.get('[data-testid="target-group-next"]').attributes('disabled')).toBeUndefined()
  })
})
