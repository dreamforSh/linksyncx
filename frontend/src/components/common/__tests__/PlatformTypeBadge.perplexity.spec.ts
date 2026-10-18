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

describe('PlatformTypeBadge Perplexity', () => {
  it('shows the shared Private badge for incognito-by-default cookie accounts', () => {
    const wrapper = mount(PlatformTypeBadge, {
      props: { platform: 'perplexity', type: 'apikey', privacyMode: 'incognito' },
    })
    expect(wrapper.text()).toContain('Private')
    expect(wrapper.html()).toContain('bg-green-100')
    expect(wrapper.find('[title="admin.accounts.privacyPerplexityIncognito"]').exists()).toBe(true)
  })

  it('hides the privacy badge when incognito is off and ignores OAuth privacy states', () => {
    for (const privacyMode of [undefined, 'training_off']) {
      const wrapper = mount(PlatformTypeBadge, {
        props: { platform: 'perplexity', type: 'apikey', privacyMode },
      })
      expect(wrapper.text()).not.toContain('Private')
    }
  })

  it('labels subscription tiers from the session snapshot', () => {
    const max = mount(PlatformTypeBadge, { props: { platform: 'perplexity', type: 'apikey', planType: 'max' } })
    expect(max.text()).toContain('Max')
    expect(max.html()).toContain('bg-amber-100')

    const pro = mount(PlatformTypeBadge, { props: { platform: 'perplexity', type: 'apikey', planType: 'pro' } })
    expect(pro.text()).toContain('Pro')
  })
})
