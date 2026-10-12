import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UtilizationBar from '../UtilizationBar.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

describe('UtilizationBar', () => {
  it('上游没有该窗口时显示「无限额」而不是「暂无数据」', () => {
    const wrapper = mount(UtilizationBar, {
      props: { label: '5h', window: null, absent: true, absentHint: 'no 5h window' }
    })
    expect(wrapper.text()).toContain('groupManagement.accountUsage.noLimit')
    expect(wrapper.text()).not.toContain('groupManagement.accountUsage.noData')
    expect(wrapper.get('[title="no 5h window"]').exists()).toBe(true)
    expect(wrapper.get('[role="progressbar"]').attributes('aria-valuetext')).toBe('groupManagement.accountUsage.noLimit')
  })

  it('没有数据且未标记缺席时仍显示「暂无数据」', () => {
    const wrapper = mount(UtilizationBar, { props: { label: '5h', window: null } })
    expect(wrapper.text()).toContain('groupManagement.accountUsage.noData')
  })

  it('有窗口数据时优先显示百分比', () => {
    const wrapper = mount(UtilizationBar, {
      props: { label: '7d', window: { utilization: 42, remaining_seconds: 0 }, absent: true }
    })
    expect(wrapper.text()).toContain('42%')
    expect(wrapper.text()).not.toContain('groupManagement.accountUsage.noLimit')
  })
})
