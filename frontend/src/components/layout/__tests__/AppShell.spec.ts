import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, h, nextTick, ref, shallowRef, type Component } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../AppSidebar.vue', () => ({
  default: defineComponent({ name: 'AppSidebar', render: () => h('aside', { class: 'sidebar-stub' }) })
}))
vi.mock('../AppHeader.vue', () => ({
  default: defineComponent({ name: 'AppHeader', render: () => h('header', { class: 'header-stub' }) })
}))
vi.mock('../AppOnboardingTour', () => ({
  default: defineComponent({ name: 'AppOnboardingTour', render: () => null })
}))

import AppShell from '../AppShell.vue'
import AppLayout from '../AppLayout.vue'

const PageA = defineComponent({
  name: 'PageA',
  render: () => h(AppLayout, null, { default: () => h('p', { class: 'page-a' }, 'A') })
})
const PageB = defineComponent({
  name: 'PageB',
  render: () => h(AppLayout, null, { default: () => h('p', { class: 'page-b' }, 'B') })
})
const BarePage = defineComponent({
  name: 'BarePage',
  render: () => h('p', { class: 'bare-page' }, 'bare')
})

function mountShell(initial: Component) {
  const current = shallowRef<Component>(initial)
  const wrapper = mount(
    defineComponent({
      setup: () => () => h(AppShell, null, { default: () => h(current.value) })
    })
  )
  return { wrapper, current }
}

describe('AppShell persistent layout', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('stays transparent while no page uses AppLayout', () => {
    const { wrapper } = mountShell(BarePage)

    expect(wrapper.find('.sidebar-stub').exists()).toBe(false)
    expect(wrapper.find('.header-stub').exists()).toBe(false)
    expect(wrapper.find('[role="main"]').exists()).toBe(false)
    expect(wrapper.find('.bare-page').exists()).toBe(true)
  })

  it('keeps the sidebar and header mounted when switching between layout pages', async () => {
    const { wrapper, current } = mountShell(PageA)
    await nextTick()

    const sidebar = wrapper.find('.sidebar-stub').element
    const header = wrapper.find('.header-stub').element
    expect(sidebar).toBeTruthy()
    expect(wrapper.find('[role="main"] .page-a').exists()).toBe(true)

    current.value = PageB
    await nextTick()
    await nextTick()

    expect(wrapper.find('.page-b').exists()).toBe(true)
    expect(wrapper.find('.sidebar-stub').element).toBe(sidebar)
    expect(wrapper.find('.header-stub').element).toBe(header)
  })

  it('drops the chrome when navigating to a page without AppLayout', async () => {
    const { wrapper, current } = mountShell(PageA)
    await nextTick()
    expect(wrapper.find('.sidebar-stub').exists()).toBe(true)

    current.value = BarePage
    await nextTick()
    await nextTick()

    expect(wrapper.find('.sidebar-stub').exists()).toBe(false)
    expect(wrapper.find('.bare-page').exists()).toBe(true)
  })

  it('follows conditional AppLayout usage inside the same page instance', async () => {
    const fullscreen = ref(false)
    const ConditionalPage = defineComponent({
      setup: () => () =>
        fullscreen.value
          ? h('div', { class: 'fullscreen' }, 'full')
          : h(AppLayout, null, { default: () => h('p', { class: 'embedded' }, 'embedded') })
    })
    const { wrapper } = mountShell(ConditionalPage)
    await nextTick()
    expect(wrapper.find('.sidebar-stub').exists()).toBe(true)

    fullscreen.value = true
    await nextTick()
    await nextTick()
    expect(wrapper.find('.sidebar-stub').exists()).toBe(false)
    expect(wrapper.find('.fullscreen').exists()).toBe(true)
  })

  it('renders the full layout by itself when there is no outer shell', () => {
    const wrapper = mount(AppLayout, { slots: { default: () => h('p', { class: 'solo' }, 'solo') } })

    expect(wrapper.find('.sidebar-stub').exists()).toBe(true)
    expect(wrapper.find('.header-stub').exists()).toBe(true)
    expect(wrapper.find('[role="main"] .solo').exists()).toBe(true)
  })
})
