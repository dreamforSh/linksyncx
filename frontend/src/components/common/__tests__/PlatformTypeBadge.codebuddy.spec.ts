import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import PlatformTypeBadge from '../PlatformTypeBadge.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

describe('PlatformTypeBadge CodeBuddy', () => {
  it('labels the subscription tier from account_type', () => {
    const cases: Array<[string, string]> = [
      ['personal', 'Personal'],
      ['ultimate', 'Ultimate'],
      ['exclusive', 'Exclusive'],
    ]
    for (const [planType, label] of cases) {
      const wrapper = mount(PlatformTypeBadge, {
        props: { platform: 'codebuddy', type: 'oauth', planType },
      })
      expect(wrapper.text()).toContain(label)
      // Tier badge uses the CodeBuddy platform color (violet), not the gray/other defaults.
      expect(wrapper.html()).toContain('bg-violet-100')
    }
  })

  it('keeps the exclusive (private) tier on the platform color', () => {
    const wrapper = mount(PlatformTypeBadge, {
      props: { platform: 'codebuddy', type: 'oauth', planType: 'exclusive' },
    })
    expect(wrapper.text()).toContain('Exclusive')
    expect(wrapper.html()).toContain('text-violet-600')
    expect(wrapper.html()).not.toContain('bg-gray-100')
  })

  it('renders no tier badge when account_type is absent', () => {
    const wrapper = mount(PlatformTypeBadge, {
      props: { platform: 'codebuddy', type: 'oauth' },
    })
    expect(wrapper.text()).not.toContain('Personal')
    expect(wrapper.text()).not.toContain('Ultimate')
  })
})
