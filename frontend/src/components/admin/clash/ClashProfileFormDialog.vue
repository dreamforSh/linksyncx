<template>
  <BaseDialog
    :show="show"
    :title="dialogTitle"
    width="wide"
    @close="handleClose"
  >
    <!-- Result of a fresh create: summary of the first synchronous refresh -->
    <div v-if="createdResult" class="space-y-4" data-testid="clash-create-result">
      <div class="flex items-start gap-3 rounded-xl border border-emerald-200 bg-emerald-50 p-4 dark:border-emerald-500/30 dark:bg-emerald-500/10">
        <Icon name="checkCircle" size="md" class="mt-0.5 flex-shrink-0 text-emerald-600 dark:text-emerald-400" />
        <div class="min-w-0 text-sm text-emerald-900 dark:text-emerald-200">
          <p class="font-medium">{{ t('admin.clash.createResult.created', { name: createdResult.profile.name }) }}</p>
          <p class="mt-0.5 text-emerald-800/80 dark:text-emerald-300/80">{{ createdSummaryText }}</p>
        </div>
      </div>
      <ClashRefreshSummary v-if="createdResult.refresh" :result="createdResult.refresh" />
    </div>

    <form v-else id="clash-profile-form" class="space-y-4" novalidate @submit.prevent="handleSubmit">
      <div
        v-if="encryptionKeyMissing"
        role="alert"
        class="flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
        data-testid="clash-encryption-key-alert"
      >
        <Icon name="lock" size="sm" class="mt-0.5 flex-shrink-0" />
        <span>{{ t('admin.clash.errors.encryptionKeyRequired') }}</span>
      </div>

      <div class="grid grid-cols-1 gap-4 md:grid-cols-[1fr_auto]">
        <div>
          <label class="input-label" for="clash-profile-name">{{ t('admin.clash.form.name') }} <span class="text-red-500">*</span></label>
          <input
            id="clash-profile-name"
            v-model="form.name"
            type="text"
            class="input"
            maxlength="100"
            :class="errors.name && 'input-error'"
            :placeholder="t('admin.clash.form.namePlaceholder')"
          />
          <p v-if="errors.name" class="input-error-text">{{ errors.name }}</p>
        </div>
        <div class="flex items-center gap-3 md:pt-6">
          <Toggle v-model="form.enabled" :aria-label="t('admin.clash.form.enabled')" />
          <div>
            <p class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.clash.form.enabled') }}</p>
            <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.clash.form.enabledHint') }}</p>
          </div>
        </div>
      </div>

      <div v-if="!isEdit">
        <span class="input-label">{{ t('admin.clash.form.source') }}</span>
        <SegmentedControl
          v-model="form.sourceType"
          :options="sourceOptions"
          :aria-label="t('admin.clash.form.source')"
          test-id="clash-profile-source"
          @change="onSourceChange"
        />
      </div>

      <div v-if="isFileSource" data-testid="clash-profile-file">
        <span class="input-label">
          {{ t('admin.clash.form.file') }}
          <span v-if="!isEdit" class="text-red-500">*</span>
        </span>
        <div
          class="flex flex-col items-center justify-center gap-2 rounded-xl border-2 border-dashed px-4 py-5 text-center transition-colors"
          :class="dragActive
            ? 'border-primary-400 bg-primary-50/60 dark:border-primary-500/60 dark:bg-primary-500/10'
            : errors.file ? 'border-red-300 dark:border-red-500/50' : 'border-gray-300 dark:border-dark-600'"
          data-testid="clash-profile-dropzone"
          @dragenter.prevent="dragActive = true"
          @dragover.prevent="dragActive = true"
          @dragleave.prevent="dragActive = false"
          @drop.prevent="onFileDrop"
        >
          <template v-if="file.name">
            <div class="flex max-w-full items-center gap-2 text-sm text-gray-900 dark:text-white">
              <Icon name="document" size="md" class="flex-shrink-0 text-primary-500" />
              <span class="truncate font-medium" :title="file.name" data-testid="clash-profile-file-name">{{ file.name }}</span>
              <span class="flex-shrink-0 text-xs text-gray-500 dark:text-dark-400">{{ formatBytes(file.size, 1) }}</span>
            </div>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="reading" @click="pickFile">
              {{ t('admin.clash.form.fileReplace') }}
            </button>
          </template>
          <template v-else>
            <Icon name="upload" size="lg" class="text-gray-400 dark:text-dark-500" />
            <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('admin.clash.form.fileDrop') }}</p>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="reading" data-testid="clash-profile-file-pick" @click="pickFile">
              {{ reading ? t('admin.clash.form.fileReading') : t('admin.clash.form.filePick') }}
            </button>
          </template>
          <input
            ref="fileInputRef"
            type="file"
            class="hidden"
            accept=".yaml,.yml,.txt,text/yaml,application/yaml,application/x-yaml,text/plain"
            data-testid="clash-profile-file-input"
            @change="onFileInput"
          />
        </div>
        <p v-if="errors.file" class="input-error-text" data-testid="clash-profile-file-error">{{ errors.file }}</p>
        <p v-else-if="isEdit" class="input-hint">
          {{ t('admin.clash.form.fileKeepHint', { name: profile?.source_name || '-', size: formatBytes(profile?.source_size ?? 0, 1) }) }}
        </p>
        <p v-else class="input-hint">{{ t('admin.clash.form.fileHint') }}</p>
      </div>

      <div v-else-if="isLinksSource" data-testid="clash-profile-links">
        <label class="input-label" for="clash-profile-links">
          {{ t('admin.clash.form.links') }}
          <span v-if="!isEdit" class="text-red-500">*</span>
        </label>
        <textarea
          id="clash-profile-links"
          ref="linksInputRef"
          v-model="form.links"
          rows="4"
          class="input font-mono text-xs leading-relaxed"
          :class="errors.links && 'input-error'"
          autocomplete="off"
          spellcheck="false"
          :placeholder="t('admin.clash.form.linksPlaceholder')"
          data-testid="clash-profile-links-input"
          @input="onLinksInput"
        ></textarea>
        <p v-if="errors.links" class="input-error-text" data-testid="clash-profile-links-error">{{ errors.links }}</p>
        <template v-else>
          <p v-if="linksDetected" class="mt-1 text-xs text-emerald-600 dark:text-emerald-400" data-testid="clash-profile-links-detected">
            {{ t('admin.clash.form.linksDetected') }}
          </p>
          <p class="input-hint">
            {{ isEdit ? t('admin.clash.form.linksKeepHint', { count: profile?.node_count ?? 0 }) : t('admin.clash.form.linksHint') }}
          </p>
        </template>
      </div>

      <div v-else>
        <label class="input-label" for="clash-profile-url">
          {{ t('admin.clash.form.url') }}
          <span v-if="!isEdit" class="text-red-500">*</span>
        </label>
        <input
          id="clash-profile-url"
          v-model="form.url"
          type="url"
          class="input font-mono text-sm"
          autocomplete="off"
          spellcheck="false"
          :class="errors.url && 'input-error'"
          :placeholder="isEdit ? profile?.url_masked : 'https://'"
          data-testid="clash-profile-url"
          @paste="onUrlPaste"
        />
        <p v-if="errors.url" class="input-error-text" data-testid="clash-profile-url-error">
          {{ errors.url }}
          <button
            v-if="!isEdit && urlIsNodeLink"
            type="button"
            class="ml-1 font-medium underline decoration-dotted underline-offset-2"
            data-testid="clash-profile-use-links"
            @click="switchToLinks(form.url)"
          >
            {{ t('admin.clash.form.useLinks') }}
          </button>
        </p>
        <p v-else-if="isEdit" class="input-hint">{{ t('admin.clash.form.urlKeepHint', { masked: profile?.url_masked || '-' }) }}</p>
        <p v-else class="input-hint">{{ t('admin.clash.form.urlHint') }}</p>
      </div>

      <div v-if="!isLocalSource" class="grid grid-cols-1 gap-4 md:grid-cols-2">
        <div>
          <label class="input-label" for="clash-profile-ua">{{ t('admin.clash.form.userAgent') }}</label>
          <input
            id="clash-profile-ua"
            v-model="form.userAgent"
            type="text"
            class="input font-mono text-sm"
            maxlength="200"
            :placeholder="t('admin.clash.form.userAgentPlaceholder')"
          />
        </div>
        <div>
          <label class="input-label">{{ t('admin.clash.form.interval') }}</label>
          <div class="flex gap-2">
            <div class="min-w-0 flex-1">
              <Select v-model="form.intervalPreset" :options="intervalOptions" :aria-label="t('admin.clash.form.interval')" />
            </div>
            <input
              v-if="form.intervalPreset === 'custom'"
              v-model.number="form.customMinutes"
              type="number"
              min="10"
              max="10080"
              class="input w-32"
              :class="errors.interval && 'input-error'"
              :aria-label="t('admin.clash.form.customMinutes')"
              data-testid="clash-profile-custom-minutes"
            />
          </div>
          <p v-if="errors.interval" class="input-error-text">{{ errors.interval }}</p>
          <p v-else-if="form.intervalPreset === 'custom'" class="input-hint">{{ t('admin.clash.form.customMinutesHint') }}</p>
        </div>
      </div>

      <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
        <div>
          <label class="input-label" for="clash-profile-include">{{ t('admin.clash.form.include') }}</label>
          <input
            id="clash-profile-include"
            v-model="form.includePattern"
            type="text"
            class="input font-mono text-sm"
            maxlength="1000"
            spellcheck="false"
            :placeholder="t('admin.clash.form.includePlaceholder')"
          />
          <p class="input-hint">{{ t('admin.clash.form.includeHint') }}</p>
        </div>
        <div>
          <label class="input-label" for="clash-profile-exclude">{{ t('admin.clash.form.exclude') }}</label>
          <label v-if="!isEdit" class="mb-2 flex items-start gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input
              v-model="form.useDefaultExclude"
              type="checkbox"
              class="mt-0.5 h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600"
              data-testid="clash-profile-default-exclude"
            />
            <span>{{ t('admin.clash.form.useDefaultExclude') }}</span>
          </label>
          <input
            v-if="isEdit || !form.useDefaultExclude"
            id="clash-profile-exclude"
            v-model="form.excludePattern"
            type="text"
            class="input font-mono text-sm"
            maxlength="1000"
            spellcheck="false"
          />
          <p class="input-hint">
            {{ isEdit || !form.useDefaultExclude ? t('admin.clash.form.excludeHint') : t('admin.clash.form.excludeDefaultHint') }}
          </p>
        </div>
      </div>

      <div v-if="!isLocalSource">
        <label class="input-label">{{ t('admin.clash.form.fetchProxy') }}</label>
        <Select
          v-model="form.fetchProxyId"
          :options="fetchProxyOptions"
          searchable
          :aria-label="t('admin.clash.form.fetchProxy')"
        />
        <p class="input-hint">{{ t('admin.clash.form.fetchProxyHint') }}</p>
      </div>

      <div>
        <label class="input-label" for="clash-profile-notes">{{ t('admin.clash.form.notes') }}</label>
        <textarea
          id="clash-profile-notes"
          v-model="form.notes"
          rows="2"
          maxlength="2000"
          class="input"
          :placeholder="t('admin.clash.form.notesPlaceholder')"
        ></textarea>
      </div>

      <!-- Dry-run parse -->
      <div class="rounded-xl border border-dashed border-gray-300 p-4 dark:border-dark-600" data-testid="clash-preview">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.clash.preview.title') }}</p>
            <p class="text-xs text-gray-500 dark:text-dark-400">{{ previewHint }}</p>
          </div>
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="previewing || submitting || reading || !canPreview"
            data-testid="clash-preview-button"
            @click="runPreview"
          >
            <Icon name="beaker" size="sm" :class="previewing ? 'animate-pulse' : ''" />
            {{ previewing ? t('admin.clash.preview.running') : t('admin.clash.actions.preview') }}
          </button>
        </div>

        <p v-if="previewError" role="alert" class="mt-3 rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-500/10 dark:text-red-300">
          {{ previewError }}
        </p>

        <div v-if="preview" class="mt-4 space-y-3" data-testid="clash-preview-result">
          <dl class="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div class="preview-stat">
              <dt>{{ t('admin.clash.preview.format') }}</dt>
              <dd>{{ formatName(preview.format) }}</dd>
            </div>
            <div class="preview-stat">
              <dt>{{ t('admin.clash.preview.nodeCount') }}</dt>
              <dd class="tabular-nums">{{ preview.node_count }}</dd>
            </div>
            <div class="preview-stat">
              <dt>{{ t('admin.clash.preview.usable') }}</dt>
              <dd class="tabular-nums text-emerald-600 dark:text-emerald-400">{{ preview.usable }}</dd>
            </div>
            <div class="preview-stat">
              <dt>{{ t('admin.clash.preview.excluded') }}</dt>
              <dd class="tabular-nums">{{ excludedCount }}</dd>
            </div>
          </dl>

          <p v-if="preview.user_info" class="text-xs text-gray-600 dark:text-gray-400">
            <template v-if="preview.user_info.total > 0">
              {{ t('admin.clash.preview.traffic', {
                used: formatBytes(preview.user_info.upload + preview.user_info.download, 1),
                total: formatBytes(preview.user_info.total, 1)
              }) }}
            </template>
            <template v-if="preview.user_info.expire">
              <span v-if="preview.user_info.total > 0" class="mx-1.5 text-gray-300 dark:text-dark-600">·</span>
              {{ t('admin.clash.preview.expire', { date: formatDateOnly(preview.user_info.expire) }) }}
            </template>
          </p>

          <div class="max-h-64 overflow-auto rounded-lg border border-gray-100 dark:border-dark-700">
            <table class="w-full text-left text-xs">
              <thead class="sticky top-0 bg-gray-50 text-gray-500 dark:bg-dark-800 dark:text-dark-400">
                <tr>
                  <th class="px-3 py-2 font-medium">{{ t('admin.clash.preview.columns.name') }}</th>
                  <th class="px-3 py-2 font-medium">{{ t('admin.clash.preview.columns.type') }}</th>
                  <th class="px-3 py-2 font-medium">{{ t('admin.clash.preview.columns.server') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr
                  v-for="(node, index) in preview.nodes"
                  :key="`${node.name}-${index}`"
                  :class="node.excluded ? 'text-gray-400 dark:text-dark-500' : 'text-gray-700 dark:text-gray-300'"
                  :data-excluded="node.excluded ? 'true' : undefined"
                >
                  <td class="px-3 py-1.5">
                    <span :class="node.excluded && 'line-through'">{{ node.name }}</span>
                    <span v-if="node.excluded" class="ml-1.5 rounded bg-gray-100 px-1 py-px text-[10px] text-gray-500 no-underline dark:bg-dark-700 dark:text-dark-400">
                      {{ t('admin.clash.preview.excludedTag') }}
                    </span>
                  </td>
                  <td class="px-3 py-1.5 font-mono uppercase">{{ node.type }}</td>
                  <td class="px-3 py-1.5 font-mono">{{ node.server }}:{{ node.port }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-if="preview.nodes.length < preview.node_count" class="text-xs text-gray-500 dark:text-dark-400">
            {{ t('admin.clash.preview.truncated', { shown: preview.nodes.length, total: preview.node_count }) }}
          </p>

          <div v-if="preview.skipped?.length" class="rounded-lg bg-amber-50 p-3 text-xs text-amber-800 dark:bg-amber-500/10 dark:text-amber-200">
            <p class="font-medium">{{ t('admin.clash.preview.skipped', { count: preview.skipped.length }) }}</p>
            <ul class="mt-1 space-y-0.5">
              <li v-for="(item, index) in preview.skipped" :key="`${item.name}-${index}`" class="break-all">
                {{ item.name || '-' }} — {{ item.reason }}
              </li>
            </ul>
          </div>
        </div>
      </div>

      <p v-if="formError" role="alert" class="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-500/10 dark:text-red-300" data-testid="clash-form-error">
        {{ formError }}
      </p>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <template v-if="createdResult">
          <button type="button" class="btn btn-primary" data-testid="clash-create-done" @click="handleClose">
            {{ t('admin.clash.createResult.done') }}
          </button>
        </template>
        <template v-else>
          <button type="button" class="btn btn-secondary" :disabled="submitting" @click="handleClose">
            {{ t('common.cancel') }}
          </button>
          <button
            type="submit"
            form="clash-profile-form"
            class="btn btn-primary"
            :disabled="submitting"
            data-testid="clash-profile-submit"
          >
            <Icon v-if="submitting" name="refresh" size="sm" class="animate-spin" />
            {{ submitLabel }}
          </button>
        </template>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import ClashRefreshSummary from './ClashRefreshSummary.vue'
import type {
  ClashCreateProfileResult,
  ClashPreviewResult,
  ClashProfile,
  ClashProfileInput,
  ClashProfileSourceType,
  ClashSubscriptionFormat,
  Proxy
} from '@/types'
import { CLASH_FILE_MAX_BYTES } from '@/api/admin/clash'
import { extractApiErrorMessage } from '@/utils/apiError'
import { clashErrorCode, clashErrorMessage, isClashNodeLinkText, summarizeClashNodeLinks } from '@/utils/clash'
import { formatBytes, formatDateOnly } from '@/utils/format'

type IntervalPreset = 0 | 60 | 180 | 360 | 720 | 1440 | 'custom'

const INTERVAL_PRESETS: Exclude<IntervalPreset, 'custom'>[] = [0, 60, 180, 360, 720, 1440]
const DEFAULT_INTERVAL = 360
const MIN_INTERVAL = 10
const MAX_INTERVAL = 10080

const props = withDefaults(
  defineProps<{
    show: boolean
    profile?: ClashProfile | null
    proxies?: Proxy[]
  }>(),
  { profile: null, proxies: () => [] }
)

const emit = defineEmits<{
  (e: 'close'): void
  /** reparse: a new file was uploaded and only takes effect once the subscription is refreshed. */
  (e: 'saved', payload: { profile: ClashProfile; created: boolean; reparse?: boolean }): void
}>()

const { t } = useI18n()
const appStore = useAppStore()

const form = reactive({
  name: '',
  sourceType: 'url' as ClashProfileSourceType,
  url: '',
  /** Pasted node share links, one per line. */
  links: '',
  userAgent: '',
  enabled: true,
  intervalPreset: DEFAULT_INTERVAL as IntervalPreset,
  customMinutes: 30,
  includePattern: '',
  useDefaultExclude: true,
  excludePattern: '',
  fetchProxyId: null as number | null,
  notes: ''
})
const errors = reactive({ name: '', url: '', interval: '', file: '', links: '' })
const linksInputRef = ref<HTMLTextAreaElement | null>(null)
/** Node links pasted into the URL box were moved to the links source. */
const linksDetected = ref(false)
/** The uploaded configuration, read in the browser and sent as text. */
const file = reactive({ name: '', size: 0, content: '' })
const reading = ref(false)
const dragActive = ref(false)
const fileInputRef = ref<HTMLInputElement | null>(null)
const submitting = ref(false)
const formError = ref('')
const encryptionKeyMissing = ref(false)
const previewing = ref(false)
const preview = ref<ClashPreviewResult | null>(null)
const previewError = ref('')
const createdResult = ref<ClashCreateProfileResult | null>(null)

const isEdit = computed(() => props.profile !== null)
// The source of a saved subscription never changes.
const sourceType = computed<ClashProfileSourceType>(() => (props.profile ? props.profile.source_type ?? 'url' : form.sourceType))
const isFileSource = computed(() => sourceType.value === 'file')
const isLinksSource = computed(() => sourceType.value === 'links')
/** Uploaded files and pasted links: nothing to fetch, so no User-Agent, schedule or fetch proxy. */
const isLocalSource = computed(() => sourceType.value !== 'url')
const urlIsNodeLink = computed(() => isClashNodeLinkText(form.url))

const sourceOptions = computed(() => [
  { value: 'url' as ClashProfileSourceType, label: t('admin.clash.form.sourceUrl'), icon: 'link' as const },
  { value: 'file' as ClashProfileSourceType, label: t('admin.clash.form.sourceFile'), icon: 'document' as const },
  { value: 'links' as ClashProfileSourceType, label: t('admin.clash.form.sourceLinks'), icon: 'server' as const }
])

const canPreview = computed(() => {
  if (isFileSource.value) return file.content !== ''
  if (isLinksSource.value) return form.links.trim() !== ''
  return form.url.trim() !== ''
})

const previewHint = computed(() => {
  if (isFileSource.value) return isEdit.value ? t('admin.clash.preview.editFileHint') : t('admin.clash.preview.fileHint')
  if (isLinksSource.value) return isEdit.value ? t('admin.clash.preview.editLinksHint') : t('admin.clash.preview.linksHint')
  return isEdit.value ? t('admin.clash.preview.editHint') : t('admin.clash.preview.hint')
})

const dialogTitle = computed(() => {
  if (createdResult.value) return t('admin.clash.createResult.title')
  return isEdit.value ? t('admin.clash.form.editTitle') : t('admin.clash.form.createTitle')
})

const submitLabel = computed(() => {
  if (submitting.value) {
    if (isEdit.value) return t('common.saving')
    if (isFileSource.value) return t('admin.clash.form.creatingFile')
    return isLinksSource.value ? t('admin.clash.form.creatingLinks') : t('admin.clash.form.creating')
  }
  if (isEdit.value) return t('common.save')
  return isLocalSource.value ? t('admin.clash.form.submitCreateFile') : t('admin.clash.form.submitCreate')
})

const intervalOptions = computed(() => [
  ...INTERVAL_PRESETS.map((minutes) => ({
    value: minutes,
    label: minutes === 0
      ? t('admin.clash.interval.manual')
      : t('admin.clash.interval.everyHours', { hours: minutes / 60 })
  })),
  { value: 'custom', label: t('admin.clash.interval.custom') }
])

const fetchProxyOptions = computed(() => {
  const options: Array<{ value: number | null; label: string }> = [
    { value: null, label: t('admin.clash.form.fetchProxyNone') }
  ]
  for (const proxy of props.proxies) {
    options.push({ value: proxy.id, label: `${proxy.name} (${proxy.protocol}://${proxy.host}:${proxy.port})` })
  }
  const current = form.fetchProxyId
  if (current !== null && !props.proxies.some((proxy) => proxy.id === current)) {
    options.push({ value: current, label: t('admin.clash.selector.unlistedProxy', { id: current }) })
  }
  return options
})

const excludedCount = computed(() => preview.value?.nodes.filter((node) => node.excluded).length ?? 0)

const createdSummaryText = computed(() => {
  const result = createdResult.value
  if (!result) return ''
  const refresh = result.refresh
  if (!refresh) return t('admin.clash.createResult.notRefreshed')
  if (refresh.status === 'ok') {
    if (result.profile.source_type === 'file') return t('admin.clash.createResult.fileParsed')
    if (result.profile.source_type === 'links') return t('admin.clash.createResult.linksParsed')
    return t('admin.clash.createResult.refreshOk')
  }
  if (refresh.status === 'skipped') return t('admin.clash.createResult.refreshSkipped')
  return t('admin.clash.createResult.refreshFailed')
})

const formatName = (format: ClashSubscriptionFormat) => {
  switch (format) {
    case 'clash_yaml':
      return t('admin.clash.formats.clashYaml')
    case 'base64_yaml':
      return t('admin.clash.formats.base64Yaml')
    case 'uri_list':
      return t('admin.clash.formats.uriList')
    default:
      return '-'
  }
}

function resetForm() {
  const profile = props.profile
  form.name = profile?.name ?? ''
  form.sourceType = profile?.source_type ?? 'url'
  form.url = ''
  form.links = ''
  linksDetected.value = false
  autoName = ''
  form.userAgent = profile?.user_agent ?? ''
  form.enabled = profile?.enabled ?? true
  const interval = profile?.refresh_interval_minutes ?? DEFAULT_INTERVAL
  if ((INTERVAL_PRESETS as number[]).includes(interval)) {
    form.intervalPreset = interval as IntervalPreset
    form.customMinutes = 30
  } else {
    form.intervalPreset = 'custom'
    form.customMinutes = interval
  }
  form.includePattern = profile?.include_pattern ?? ''
  form.useDefaultExclude = true
  form.excludePattern = profile?.exclude_pattern ?? ''
  form.fetchProxyId = profile?.fetch_proxy_id ?? null
  form.notes = profile?.notes ?? ''
  errors.name = ''
  errors.url = ''
  errors.interval = ''
  errors.file = ''
  errors.links = ''
  file.name = ''
  file.size = 0
  file.content = ''
  reading.value = false
  dragActive.value = false
  formError.value = ''
  encryptionKeyMissing.value = false
  preview.value = null
  previewError.value = ''
  createdResult.value = null
}

watch(
  () => props.show,
  (visible) => {
    if (visible) resetForm()
  },
  { immediate: true }
)

const isHttpUrl = (value: string) => /^https?:\/\/\S+$/i.test(value)

function intervalMinutes(): number {
  return form.intervalPreset === 'custom' ? Math.round(Number(form.customMinutes)) : form.intervalPreset
}

function validate(): boolean {
  errors.name = ''
  errors.url = ''
  errors.interval = ''
  errors.file = ''
  errors.links = ''
  const name = form.name.trim()
  if (!name) errors.name = t('admin.clash.form.validation.nameRequired')
  if (isFileSource.value) {
    // Editing without a new file keeps the stored one.
    if (!file.content && !isEdit.value) errors.file = t('admin.clash.form.validation.fileRequired')
    return !errors.name && !errors.file
  }
  if (isLinksSource.value) {
    // Editing without links keeps the stored ones.
    const links = form.links.trim()
    if (!links && !isEdit.value) errors.links = t('admin.clash.form.validation.linksRequired')
    else if (new Blob([links]).size > CLASH_FILE_MAX_BYTES) {
      errors.links = t('admin.clash.form.validation.linksTooLarge', { max: formatBytes(CLASH_FILE_MAX_BYTES, 0) })
    }
    return !errors.name && !errors.links
  }
  const url = form.url.trim()
  if (!url && !isEdit.value) errors.url = t('admin.clash.form.validation.urlRequired')
  else if (url && !isHttpUrl(url)) {
    if (!isClashNodeLinkText(url)) errors.url = t('admin.clash.form.validation.urlInvalid')
    else errors.url = isEdit.value ? t('admin.clash.form.validation.urlIsNodeLinkEdit') : t('admin.clash.form.validation.urlIsNodeLink')
  }
  if (form.intervalPreset === 'custom') {
    const minutes = intervalMinutes()
    if (!Number.isFinite(minutes) || minutes < MIN_INTERVAL || minutes > MAX_INTERVAL) {
      errors.interval = t('admin.clash.form.validation.intervalRange', { min: MIN_INTERVAL, max: MAX_INTERVAL })
    }
  }
  return !errors.name && !errors.url && !errors.interval
}

/** Files and pasted links carry no url, User-Agent, schedule or fetch proxy. */
function buildLocalPayload(): ClashProfileInput {
  const payload: ClashProfileInput = {
    name: form.name.trim(),
    enabled: form.enabled,
    include_pattern: form.includePattern.trim(),
    notes: form.notes.trim()
  }
  if (isEdit.value) {
    payload.exclude_pattern = form.excludePattern.trim()
  } else {
    payload.source_type = sourceType.value
    if (!form.useDefaultExclude) payload.exclude_pattern = form.excludePattern.trim()
  }
  const links = form.links.trim()
  if (isFileSource.value && file.content) {
    payload.content = file.content
    payload.source_name = file.name
  } else if (isLinksSource.value && links) {
    payload.content = links
  }
  return payload
}

function buildPayload(): ClashProfileInput {
  if (isLocalSource.value) return buildLocalPayload()
  const payload: ClashProfileInput = {
    name: form.name.trim(),
    enabled: form.enabled,
    refresh_interval_minutes: intervalMinutes(),
    include_pattern: form.includePattern.trim(),
    notes: form.notes.trim()
  }
  const url = form.url.trim()
  const userAgent = form.userAgent.trim()
  if (isEdit.value) {
    // Empty url keeps the stored link; an empty User-Agent falls back to the pool default.
    if (url) payload.url = url
    payload.user_agent = userAgent
    payload.exclude_pattern = form.excludePattern.trim()
    payload.fetch_proxy_id = form.fetchProxyId ?? 0
  } else {
    payload.url = url
    if (userAgent) payload.user_agent = userAgent
    // Omitting exclude_pattern lets the server apply its default info-node filter.
    if (!form.useDefaultExclude) payload.exclude_pattern = form.excludePattern.trim()
    if (form.fetchProxyId !== null) payload.fetch_proxy_id = form.fetchProxyId
  }
  return payload
}

function describeError(error: unknown, fallbackKey: string) {
  return clashErrorMessage(error, t) ?? extractApiErrorMessage(error, t(fallbackKey))
}

function onSourceChange() {
  errors.url = ''
  errors.file = ''
  errors.links = ''
  linksDetected.value = false
  preview.value = null
  previewError.value = ''
}

// The name picked from the links, replaced as they change until the admin types their own.
let autoName = ''

function applyLinksName() {
  if (isEdit.value) return
  const { count, firstName } = summarizeClashNodeLinks(form.links)
  if (!firstName) return
  const name = (count > 1 ? t('admin.clash.form.linksNameMany', { name: firstName, count }) : firstName).slice(0, 100)
  if (!form.name.trim() || form.name === autoName) {
    form.name = name
    autoName = name
  }
}

function onLinksInput() {
  errors.links = ''
  preview.value = null
  previewError.value = ''
  applyLinksName()
}

/** Moves node share links entered in the URL box to the links source. */
function switchToLinks(text: string) {
  const pasted = text.trim()
  const kept = form.links.trim()
  form.sourceType = 'links'
  onSourceChange()
  form.links = kept ? `${kept}\n${pasted}` : pasted
  form.url = ''
  linksDetected.value = true
  applyLinksName()
  void nextTick(() => linksInputRef.value?.focus())
}

function onUrlPaste(event: ClipboardEvent) {
  if (isEdit.value) return
  const text = event.clipboardData?.getData('text') ?? ''
  if (!isClashNodeLinkText(text)) return
  event.preventDefault()
  switchToLinks(text)
}

function pickFile() {
  fileInputRef.value?.click()
}

function readFileText(selected: File): Promise<string> {
  if (typeof selected.text === 'function') return selected.text()
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result ?? ''))
    reader.onerror = () => reject(reader.error)
    reader.readAsText(selected)
  })
}

/** Reads a chosen file; a rejected file leaves the previous choice in place. */
async function loadFile(selected: File) {
  errors.file = ''
  if (selected.size > CLASH_FILE_MAX_BYTES) {
    errors.file = t('admin.clash.form.validation.fileTooLarge', { max: formatBytes(CLASH_FILE_MAX_BYTES, 0) })
    return
  }
  reading.value = true
  try {
    const content = await readFileText(selected)
    if (!content.trim()) {
      errors.file = t('admin.clash.form.validation.fileEmpty')
      return
    }
    file.name = selected.name
    file.size = selected.size
    file.content = content
    preview.value = null
    previewError.value = ''
    if (!isEdit.value && !form.name.trim()) form.name = selected.name.replace(/\.(ya?ml|txt)$/i, '').slice(0, 100)
  } catch {
    errors.file = t('admin.clash.form.validation.fileUnreadable')
  } finally {
    reading.value = false
  }
}

async function onFileInput(event: Event) {
  const input = event.target as HTMLInputElement
  const selected = input.files?.[0]
  // Allow picking the same file again after a failed read.
  input.value = ''
  if (selected) await loadFile(selected)
}

async function onFileDrop(event: DragEvent) {
  dragActive.value = false
  const dropped = event.dataTransfer?.files?.[0]
  if (dropped) await loadFile(dropped)
}

async function runPreview() {
  let source: Pick<ClashProfileInput, 'source_type' | 'url' | 'content' | 'user_agent' | 'fetch_proxy_id'>
  if (isFileSource.value) {
    if (!file.content) return
    source = { source_type: 'file', content: file.content }
  } else if (isLinksSource.value) {
    const links = form.links.trim()
    if (!links) return
    source = { source_type: 'links', content: links }
  } else {
    const url = form.url.trim()
    if (!url) return
    if (!isHttpUrl(url)) {
      errors.url = t('admin.clash.form.validation.urlInvalid')
      return
    }
    source = { url, user_agent: form.userAgent.trim() || undefined, fetch_proxy_id: form.fetchProxyId ?? undefined }
  }
  previewing.value = true
  previewError.value = ''
  try {
    const excludePattern = isEdit.value || !form.useDefaultExclude ? form.excludePattern.trim() : undefined
    preview.value = await adminAPI.clash.previewProfile({
      ...source,
      include_pattern: form.includePattern.trim() || undefined,
      exclude_pattern: excludePattern
    })
  } catch (error) {
    preview.value = null
    previewError.value = describeError(error, 'admin.clash.preview.failed')
  } finally {
    previewing.value = false
  }
}

async function handleSubmit() {
  if (submitting.value || !validate()) return
  submitting.value = true
  formError.value = ''
  encryptionKeyMissing.value = false
  try {
    const payload = buildPayload()
    if (props.profile) {
      const updated = await adminAPI.clash.updateProfile(props.profile.id, payload)
      appStore.showSuccess(t('admin.clash.form.updated'))
      emit('saved', { profile: updated, created: false, reparse: payload.content !== undefined })
      emit('close')
    } else {
      const result = await adminAPI.clash.createProfile(payload)
      createdResult.value = result
      emit('saved', { profile: result.profile, created: true })
    }
  } catch (error) {
    if (clashErrorCode(error) === 'CLASH_ENCRYPTION_KEY_REQUIRED') {
      encryptionKeyMissing.value = true
    }
    formError.value = describeError(error, 'admin.clash.form.saveFailed')
    appStore.showError(formError.value)
  } finally {
    submitting.value = false
  }
}

function handleClose() {
  if (submitting.value) return
  emit('close')
}
</script>

<style scoped>
.preview-stat {
  @apply rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-800/60;
}

.preview-stat dt {
  @apply text-[11px] text-gray-500 dark:text-dark-400;
}

.preview-stat dd {
  @apply mt-0.5 text-sm font-semibold text-gray-900 dark:text-white;
}
</style>
