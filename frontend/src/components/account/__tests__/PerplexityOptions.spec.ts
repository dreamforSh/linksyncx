import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import PerplexityOptionsFields from '../PerplexityOptionsFields.vue'
import PerplexityUsageDetails from '../PerplexityUsageDetails.vue'
import {
  applyPerplexityOptions,
  defaultPerplexityOptions,
  readPerplexityOptions,
  validatePerplexityOptions
} from '../credentialsBuilder'
import { getPresetMappingsByPlatform } from '@/composables/useModelWhitelist'
import type { PerplexityRequestOptions } from '@/types'

const i18n = createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: {} })

function readComponent(name: string): string {
  return readFileSync(resolve(process.cwd(), `src/components/account/${name}.vue`), 'utf8')
}

describe('Perplexity search settings contract', () => {
  it('keeps generic client compatibility by default and serializes only public keys', () => {
    const options = defaultPerplexityOptions()
    expect(options).toMatchObject({ sources: ['web'], include_sources: true, include_search_progress: true, native_followups: true, strict_parameters: false })
    const credentials: Record<string, unknown> = { api_key: 'existing' }
    applyPerplexityOptions(credentials, {
      ...options,
      language: ' zh-CN ',
      sources: ['web', 'scholar', 'web'],
      internal: { last_backend_uuid: 'not-public' },
      mode: 'computer'
    } as PerplexityRequestOptions)
    expect(credentials.api_key).toBe('existing')
    expect(credentials.perplexity_options).toEqual({ ...options, language: 'zh-CN', sources: ['web', 'scholar'] })
  })

  it('reads saved false booleans and restores defaults for omitted fields', () => {
    const options = readPerplexityOptions({ perplexity_options: { include_sources: false, native_followups: false, sources: ['social'], language: 'fr-FR' } })
    expect(options).toMatchObject({ sources: ['social'], language: 'fr-FR', include_sources: false, native_followups: false, include_search_progress: true, strict_parameters: false })
  })

  it('rejects empty sources, malformed locales and invalid timezones', () => {
    expect(validatePerplexityOptions({ ...defaultPerplexityOptions(), sources: [] })).toBe('sourcesRequired')
    expect(validatePerplexityOptions({ ...defaultPerplexityOptions(), language: 'zh_CN' })).toBe('invalidLanguage')
    expect(validatePerplexityOptions({ ...defaultPerplexityOptions(), timezone: 'Invalid/Timezone' })).toBe('invalidTimezone')
    expect(validatePerplexityOptions({ ...defaultPerplexityOptions(), language: 'zh-CN', timezone: 'Asia/Shanghai' })).toBeNull()
  })

  it('emits checkbox and language changes without mutating parent state', async () => {
    const options = defaultPerplexityOptions()
    const wrapper = mount(PerplexityOptionsFields, { props: { modelValue: options }, global: { plugins: [i18n] } })
    await wrapper.get('[data-testid="perplexity-source-scholar"]').setValue(true)
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toMatchObject({ sources: ['web', 'scholar'] })
    expect(options.sources).toEqual(['web'])
    await wrapper.get('[data-testid="perplexity-language"]').setValue('zh-CN')
    expect(wrapper.emitted('update:modelValue')?.[1]?.[0]).toMatchObject({ language: 'zh-CN' })
    await wrapper.get('[data-testid="perplexity-strict_parameters"]').setValue(true)
    expect(wrapper.emitted('update:modelValue')?.[2]?.[0]).toMatchObject({ strict_parameters: true })
  })

  it('uses the shared fields and allowlisted serializer on both create and edit', () => {
    expect(readComponent('CreateAccountModal')).toContain('v-model="perplexityOptions"')
    expect(readComponent('CreateAccountModal')).toContain('applyPerplexityOptions(credentials, perplexityOptions.value)')
    expect(readComponent('EditAccountModal')).toContain('v-model="editPerplexityOptions"')
    expect(readComponent('EditAccountModal')).toContain('readPerplexityOptions(newAccount.credentials)')
    expect(readComponent('EditAccountModal')).toContain('applyPerplexityOptions(newCredentials, editPerplexityOptions.value)')
  })

  it('carries the selected proxy and CSRF cookie into the create-flow model preview', () => {
    const source = readComponent('CreateAccountModal')
    expect(source).toContain('proxy_id: form.proxy_id')
    expect(source).toContain('csrf_token: perplexityCsrfToken.value.trim()')
    expect(source).toContain('form.proxy_id != null')
  })

  it('offers dedicated Perplexity presets rather than Anthropic fallbacks', () => {
    const presets = getPresetMappingsByPlatform('perplexity')
    expect(presets.map(preset => preset.from)).toContain('sonar-pro')
    expect(presets.some(preset => preset.to === 'pplx_pro')).toBe(true)
    expect(presets.some(preset => preset.from.startsWith('claude-'))).toBe(false)
  })
})

describe('Perplexity usage snapshots', () => {
  it('renders remaining buckets and credits in the shared compact quota style', () => {
    const wrapper = mount(PerplexityUsageDetails, {
      props: { usage: {
        snapshot: {
          credits: { balance: 4000, total: 10000, currency: 'credits' },
          rate_limits: { remaining_research: 5, remaining_pro: 0, remaining_labs: -1, note: 'x' },
          session: { subscription_tier: 'pro' }
        },
        sections: {
          session: { status: 'success', fetched_at: '2026-10-16T00:00:00Z' },
          credits: { status: 'success', fetched_at: '2026-10-16T00:00:00Z' },
          rate_limits: { status: 'success', fetched_at: '2026-10-15T00:00:00Z' }
        }
      } },
      global: { plugins: [i18n] }
    })
    const rows = wrapper.findAll('[data-testid^="perplexity-bucket-"]')
    expect(rows.map(row => row.attributes('data-testid'))).toEqual(['perplexity-bucket-pro', 'perplexity-bucket-research', 'perplexity-bucket-labs'])
    expect(rows[0].text()).toContain('Pro')
    expect(rows[0].find('.text-red-600').exists()).toBe(true)
    expect(rows[1].text()).toContain('5')
    expect(rows[2].text()).toContain('∞')
    expect(wrapper.get('[data-testid="perplexity-credits"]').text()).toContain('4,000 / 10,000')
    expect(wrapper.find('[data-testid="perplexity-session-status"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="perplexity-usage-problem"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="perplexity-usage-updated"]').attributes('title')).toBeTruthy()
    expect(wrapper.find('pre').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('%')
  })

  it('dims stale sections and summarizes failed ones without claiming live usage', () => {
    const wrapper = mount(PerplexityUsageDetails, {
      props: { usage: {
        snapshot: { credits: { balance: 7 }, rate_limits: { remaining_pro: 3 } },
        sections: {
          credits: { status: 'stale', fetched_at: '2026-10-15T00:00:00Z', error: 'credits timeout' },
          rate_limits: { status: 'error', error: 'limits unavailable' }
        }
      } },
      global: { plugins: [i18n] }
    })
    expect(wrapper.get('[data-testid="perplexity-credits"]').classes()).toContain('opacity-60')
    expect(wrapper.get('[data-testid="perplexity-bucket-pro"]').classes()).toContain('opacity-60')
    const problem = wrapper.get('[data-testid="perplexity-usage-problem"]')
    expect(problem.attributes('title')).toContain('credits timeout')
    expect(problem.attributes('title')).toContain('limits unavailable')
  })

  it('treats a failed refresh as stale and only declares the session invalid when the backend says false', async () => {
    const wrapper = mount(PerplexityUsageDetails, {
      props: {
        unavailable: true,
        usage: { session_valid: false, snapshot: { credits: { balance: 7 } }, sections: { credits: { status: 'success', fetched_at: '2026-10-15T00:00:00Z' } } }
      },
      global: { plugins: [i18n] }
    })
    expect(wrapper.find('[data-testid="perplexity-session-status"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="perplexity-credits"]').classes()).toContain('opacity-60')
    expect(wrapper.get('[data-testid="perplexity-usage-problem"]').text()).toContain('staleSnapshot')

    await wrapper.setProps({ unavailable: false, usage: { snapshot: {}, sections: {}, session_valid: false } })
    expect(wrapper.get('[data-testid="perplexity-session-status"]').text()).toContain('needsReauth')
    await wrapper.setProps({ usage: { snapshot: {}, sections: {} } })
    expect(wrapper.find('[data-testid="perplexity-session-status"]').exists()).toBe(false)
  })
})
