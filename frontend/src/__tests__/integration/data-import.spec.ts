import { describe, it, expect, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ImportDataModal from '@/components/admin/account/ImportDataModal.vue'
import type { AdminGroup } from '@/types'

const showError = vi.fn()
const showSuccess = vi.fn()
const showWarning = vi.fn()

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    showWarning
  })
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({ isSimpleMode: false })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      importData: vi.fn()
    }
  }
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

const groups = [
  { id: 1, name: 'claude-pool', platform: 'anthropic', kind: 'channel', status: 'active' },
  { id: 2, name: 'openai-pool', platform: 'openai', kind: 'channel', status: 'active' },
  { id: 3, name: 'acme-claude', platform: 'anthropic', kind: 'managed', category: 'enterprise', status: 'active' },
  { id: 4, name: 'mixed-route', platform: 'composite', kind: 'channel', status: 'active' }
] as unknown as AdminGroup[]

const mountModal = () =>
  mount(ImportDataModal, {
    props: { show: true, groups },
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' }
      }
    }
  })

const makeJsonFile = (name: string, content: string, type = 'application/json') => {
  const file = new File([content], name, { type })
  Object.defineProperty(file, 'text', {
    value: () => Promise.resolve(content)
  })
  return file
}

const setInputFiles = (element: Element, files: File[]) => {
  Object.defineProperty(element, 'files', {
    value: files,
    configurable: true
  })
}

const selectTargetGroups = async (wrapper: ReturnType<typeof mountModal>, ids: number[]) => {
  for (const id of ids) {
    await wrapper.find(`[data-testid="import-target-groups"] input[type="checkbox"][value="${id}"]`).setValue(true)
  }
}

const exportFile = (name: string, accounts: Array<Record<string, unknown>>, proxies: unknown[] = []) =>
  makeJsonFile(name, JSON.stringify({ exported_at: '2026-07-05T00:00:00Z', proxies, accounts }))

describe('ImportDataModal', () => {
  beforeEach(async () => {
    showError.mockReset()
    showSuccess.mockReset()
    showWarning.mockReset()
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockReset()
  })

  it('未选择文件时提示错误', async () => {
    const wrapper = mountModal()

    await wrapper.find('form').trigger('submit')
    expect(showError).toHaveBeenCalledWith('admin.accounts.dataImportSelectFile')
  })

  it('无效 JSON 时按文件名提示解析失败', async () => {
    const { adminAPI } = await import('@/api/admin')
    const wrapper = mountModal()

    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [makeJsonFile('data.json', 'invalid json')])

    await input.trigger('change')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.accounts.dataImportParseFailedFile')
    expect(adminAPI.accounts.importData).not.toHaveBeenCalled()
  })

  it('不是导出数据的 JSON 按文件名拒绝', async () => {
    const { adminAPI } = await import('@/api/admin')
    const wrapper = mountModal()

    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [makeJsonFile('random.json', JSON.stringify({ name: 'test' }))])

    await input.trigger('change')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.accounts.dataImportInvalidFile')
    expect(adminAPI.accounts.importData).not.toHaveBeenCalled()
  })

  it('无有效 JSON 的选择不清空已有选择', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 1,
      account_failed: 0
    })

    const wrapper = mountModal()
    const input = wrapper.find('input[type="file"]')

    setInputFiles(input.element, [exportFile('valid.json', [{ name: 'a', platform: 'anthropic' }])])
    await input.trigger('change')

    setInputFiles(input.element, [new File(['hello'], 'notes.txt', { type: 'text/plain' })])
    await input.trigger('change')
    expect(showError).toHaveBeenCalledWith('admin.accounts.dataImportSelectFile')

    await selectTargetGroups(wrapper, [1])
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(adminAPI.accounts.importData).toHaveBeenCalledWith({
      data: expect.objectContaining({
        accounts: [{ name: 'a', platform: 'anthropic' }]
      }),
      group_ids: [1]
    })
  })

  it('merges multiple selected JSON files before importing', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 2,
      account_failed: 0
    })

    const wrapper = mountModal()

    const input = wrapper.find('input[type="file"]')
    const first = exportFile('first.json', [{ name: 'a', platform: 'anthropic' }])
    const second = exportFile('second.json', [{ name: 'b', platform: 'openai' }], [{ proxy_key: 'p' }])
    setInputFiles(input.element, [first, second])

    await input.trigger('change')
    await selectTargetGroups(wrapper, [1, 2])
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(adminAPI.accounts.importData).toHaveBeenCalledWith({
      data: expect.objectContaining({
        proxies: [{ proxy_key: 'p' }],
        accounts: [
          { name: 'a', platform: 'anthropic' },
          { name: 'b', platform: 'openai' }
        ]
      }),
      group_ids: [1, 2]
    })
    expect(showSuccess).toHaveBeenCalledWith('admin.accounts.dataImportSuccess')
  })

  it('部分成功时关闭弹窗仍通知父组件刷新', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 1,
      account_failed: 1
    })

    const wrapper = mountModal()
    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [
      exportFile('mixed.json', [
        { name: 'a', platform: 'anthropic' },
        { name: 'b', platform: 'anthropic' }
      ])
    ])

    await input.trigger('change')
    await selectTargetGroups(wrapper, [1])
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.accounts.dataImportCompletedWithErrors')
    expect(wrapper.emitted('imported')).toBeUndefined()

    // 第二个 btn-secondary 是 footer 的取消按钮(第一个是选择文件)
    await wrapper.findAll('button.btn-secondary')[1]!.trigger('click')

    expect(wrapper.emitted('imported')).toHaveLength(1)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('目标分组候选排除 composite 分组', () => {
    const wrapper = mountModal()
    const values = wrapper
      .findAll('[data-testid="import-target-groups"] input[type="checkbox"]')
      .map((input) => (input.element as HTMLInputElement).value)
    expect(values).toEqual(['1', '2', '3'])
  })

  it('未选目标分组时账号按未分组导入', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 1,
      account_failed: 0
    })
    const wrapper = mountModal()
    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [exportFile('data.json', [{ name: 'a', platform: 'anthropic' }])])

    await input.trigger('change')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(showError).not.toHaveBeenCalled()
    expect(adminAPI.accounts.importData).toHaveBeenCalledWith({
      data: expect.objectContaining({ accounts: [expect.objectContaining({ name: 'a' })] }),
      group_ids: []
    })
  })

  it('只有代理没有账号时无需目标分组', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 1,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 0,
      account_failed: 0
    })
    const wrapper = mountModal()
    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [exportFile('proxies.json', [], [{ proxy_key: 'p' }])])

    await input.trigger('change')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(adminAPI.accounts.importData).toHaveBeenCalledWith({
      data: expect.objectContaining({ proxies: [{ proxy_key: 'p' }], accounts: [] }),
      group_ids: []
    })
  })

  it('账号平台缺少目标分组时仍可导入（该平台账号不分组）', async () => {
    const { adminAPI } = await import('@/api/admin')
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 3,
      account_failed: 0
    })
    const wrapper = mountModal()
    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [
      exportFile('data.json', [
        { name: 'a', platform: 'anthropic' },
        { name: 'b', platform: 'openai' },
        { name: 'c', platform: 'openai' }
      ])
    ])

    await input.trigger('change')
    await flushPromises()
    await selectTargetGroups(wrapper, [1])

    const chips = wrapper.findAll('[data-testid="target-platform-coverage"] > span')
    expect(chips.map((chip) => chip.attributes('data-covered'))).toEqual(['true', 'false'])
    expect(chips[0]!.text()).toContain('× 1')
    expect(chips[1]!.text()).toContain('× 2')

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(showError).not.toHaveBeenCalled()
    expect(adminAPI.accounts.importData).toHaveBeenCalledWith(expect.objectContaining({ group_ids: [1] }))
  })

  it('同一平台同时选择管理分组和其他分组时拒绝导入', async () => {
    const { adminAPI } = await import('@/api/admin')
    const wrapper = mountModal()
    const input = wrapper.find('input[type="file"]')
    setInputFiles(input.element, [exportFile('data.json', [{ name: 'a', platform: 'anthropic' }])])

    await input.trigger('change')
    await selectTargetGroups(wrapper, [1, 3])
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.accounts.managedGroupExclusive')
    expect(adminAPI.accounts.importData).not.toHaveBeenCalled()
  })
})
