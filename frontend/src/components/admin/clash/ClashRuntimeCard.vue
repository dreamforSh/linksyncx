<template>
  <section class="card" aria-labelledby="clash-runtime-title" data-testid="clash-runtime-card">
    <div class="card-section-header">
      <div class="min-w-0">
        <h2 id="clash-runtime-title" class="card-section-title flex items-center gap-2">
          {{ t('admin.clash.runtime.title') }}
          <span v-if="runtime" :class="['status-pill', statusPill.pill]" data-testid="clash-runtime-status">
            <span class="h-1.5 w-1.5 rounded-full" :class="statusPill.dot" aria-hidden="true"></span>
            {{ statusPill.label }}
          </span>
        </h2>
        <p class="card-section-subtitle">{{ t('admin.clash.runtime.subtitle') }}</p>
      </div>
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        :disabled="resyncing || loading || !runtime || runtime.mode === 'disabled'"
        data-testid="clash-runtime-resync"
        @click="$emit('resync')"
      >
        <Icon name="sync" size="sm" :class="resyncing ? 'animate-spin' : ''" />
        {{ resyncing ? t('admin.clash.runtime.resyncing') : t('admin.clash.runtime.resync') }}
      </button>
    </div>

    <div class="px-5 pb-5">
      <div
        v-if="error && !runtime"
        role="alert"
        class="rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-500/10 dark:text-red-300"
      >
        {{ error }}
      </div>

      <div v-else-if="loading && !runtime" class="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6" aria-busy="true">
        <div v-for="index in 6" :key="index" class="skeleton h-[62px] rounded-xl" />
      </div>

      <template v-else-if="runtime">
        <dl class="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
          <div v-for="item in summaryItems" :key="item.key" class="runtime-item">
            <dt class="text-xs text-gray-500 dark:text-dark-400">{{ item.label }}</dt>
            <dd class="mt-1 truncate text-sm font-semibold text-gray-900 dark:text-white" :title="item.title || item.value">
              {{ item.value }}
              <span v-if="item.extra" class="ml-1 text-xs font-medium" :class="item.extraClass">{{ item.extra }}</span>
            </dd>
          </div>
        </dl>

        <div
          v-if="runtime.local.last_error"
          class="mt-3 flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-xs text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300"
          data-testid="clash-runtime-last-error"
        >
          <Icon name="exclamationCircle" size="sm" class="mt-px flex-shrink-0" />
          <span class="break-all">{{ t('admin.clash.runtime.lastError', { error: runtime.local.last_error }) }}</span>
        </div>

        <div v-if="runtime.instances.length > 0" class="mt-4 overflow-x-auto rounded-xl border border-gray-100 dark:border-dark-700/60">
          <table class="w-full min-w-[760px] text-left text-sm" data-testid="clash-runtime-instances">
            <thead class="bg-gray-50 text-xs font-medium text-gray-500 dark:bg-dark-800/60 dark:text-dark-400">
              <tr>
                <th class="px-3 py-2">{{ t('admin.clash.runtime.instance') }}</th>
                <th class="px-3 py-2">{{ t('admin.clash.runtime.status') }}</th>
                <th class="px-3 py-2">{{ t('admin.clash.runtime.version') }}</th>
                <th class="px-3 py-2">{{ t('admin.clash.runtime.listeners') }}</th>
                <th class="px-3 py-2">{{ t('admin.clash.runtime.configHash') }}</th>
                <th class="px-3 py-2">{{ t('admin.clash.runtime.lastApplied') }}</th>
                <th class="px-3 py-2">{{ t('admin.clash.runtime.heartbeat') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700/60">
              <tr v-for="instance in runtime.instances" :key="instance.instance_id" class="align-top">
                <td class="px-3 py-2">
                  <div class="flex items-center gap-1.5">
                    <span class="font-mono text-xs text-gray-900 dark:text-gray-100">{{ instance.instance_id }}</span>
                    <span
                      v-if="instance.instance_id === runtime.local.instance_id"
                      class="rounded bg-primary-50 px-1.5 py-px text-[10px] font-medium text-primary-700 dark:bg-primary-500/15 dark:text-primary-300"
                    >{{ t('admin.clash.runtime.thisInstance') }}</span>
                  </div>
                  <p v-if="instance.last_error" class="mt-1 max-w-md break-all text-xs text-red-600 dark:text-red-400">
                    {{ instance.last_error }}
                  </p>
                </td>
                <td class="px-3 py-2">
                  <span :class="['status-pill', instance.ready ? readyPill.pill : notReadyPill.pill]">
                    <span class="h-1.5 w-1.5 rounded-full" :class="instance.ready ? readyPill.dot : notReadyPill.dot" aria-hidden="true"></span>
                    {{ instance.ready ? t('admin.clash.runtime.ready') : t('admin.clash.runtime.notReady') }}
                  </span>
                </td>
                <td class="px-3 py-2 text-xs text-gray-700 dark:text-gray-300">{{ instance.version || '-' }}</td>
                <td class="px-3 py-2 text-xs tabular-nums text-gray-700 dark:text-gray-300">
                  {{ instance.listeners }}
                  <span v-if="instance.listener_failures > 0" class="ml-1 text-red-600 dark:text-red-400">
                    {{ t('admin.clash.runtime.listenerFailures', { count: instance.listener_failures }) }}
                  </span>
                </td>
                <td class="px-3 py-2">
                  <span class="font-mono text-xs text-gray-600 dark:text-gray-400">{{ shortHash(instance.config_hash) }}</span>
                  <span
                    v-if="instance.config_hash && runtime.local.config_hash && instance.config_hash !== runtime.local.config_hash"
                    class="ml-1.5 text-xs text-amber-600 dark:text-amber-400"
                  >{{ t('admin.clash.runtime.configDiffers') }}</span>
                </td>
                <td class="px-3 py-2 text-xs text-gray-600 dark:text-gray-400" :title="formatDateTime(instance.last_applied_at) || undefined">
                  {{ instance.last_applied_at ? formatRelativeTime(instance.last_applied_at) : '-' }}
                </td>
                <td class="px-3 py-2 text-xs text-gray-600 dark:text-gray-400" :title="formatDateTime(instance.updated_at) || undefined">
                  {{ instance.updated_at ? formatRelativeTime(instance.updated_at) : '-' }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { ClashRuntimeStatus } from '@/types'
import { formatDateTime, formatRelativeTime } from '@/utils/format'

const props = defineProps<{
  runtime: ClashRuntimeStatus | null
  loading: boolean
  error: string
  resyncing: boolean
}>()

defineEmits<{ (e: 'resync'): void }>()

const { t } = useI18n()

const readyPill = {
  pill: 'bg-emerald-50 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-400/20',
  dot: 'bg-emerald-500'
}
const notReadyPill = {
  pill: 'bg-red-50 text-red-700 ring-red-600/20 dark:bg-red-500/10 dark:text-red-300 dark:ring-red-400/20',
  dot: 'bg-red-500'
}
const offPill = {
  pill: 'bg-gray-100 text-gray-600 ring-gray-500/20 dark:bg-dark-700 dark:text-dark-300 dark:ring-dark-500/30',
  dot: 'bg-gray-400'
}

const statusPill = computed(() => {
  const runtime = props.runtime
  if (!runtime || runtime.mode === 'disabled') return { ...offPill, label: t('admin.clash.runtime.modes.disabled') }
  return runtime.local.ready
    ? { ...readyPill, label: t('admin.clash.runtime.ready') }
    : { ...notReadyPill, label: t('admin.clash.runtime.notReady') }
})

const shortHash = (hash: string) => (hash ? hash.slice(0, 12) : '-')

const modeLabel = (mode: string) => {
  if (mode === 'embedded' || mode === 'external' || mode === 'disabled') {
    return t(`admin.clash.runtime.modes.${mode}`)
  }
  return mode || '-'
}

const summaryItems = computed(() => {
  const runtime = props.runtime
  if (!runtime) return []
  const local = runtime.local
  return [
    { key: 'mode', label: t('admin.clash.runtime.mode'), value: modeLabel(runtime.mode) },
    { key: 'version', label: t('admin.clash.runtime.version'), value: local.version || '-' },
    {
      key: 'listeners',
      label: t('admin.clash.runtime.listeners'),
      value: String(local.listeners ?? 0),
      extra: local.listener_failures > 0 ? t('admin.clash.runtime.listenerFailures', { count: local.listener_failures }) : '',
      extraClass: 'text-red-600 dark:text-red-400'
    },
    {
      key: 'applied',
      label: t('admin.clash.runtime.lastApplied'),
      value: local.last_applied_at ? formatRelativeTime(local.last_applied_at) : '-',
      title: formatDateTime(local.last_applied_at)
    },
    { key: 'hash', label: t('admin.clash.runtime.configHash'), value: shortHash(local.config_hash), title: local.config_hash },
    {
      key: 'instances',
      label: t('admin.clash.runtime.instances'),
      value: String(runtime.instances.length),
      extra: unreadyCount.value > 0 ? t('admin.clash.runtime.unreadyInstances', { count: unreadyCount.value }) : '',
      extraClass: 'text-amber-600 dark:text-amber-400'
    }
  ] as Array<{ key: string; label: string; value: string; title?: string; extra?: string; extraClass?: string }>
})

const unreadyCount = computed(() => props.runtime?.instances.filter((instance) => !instance.ready).length ?? 0)
</script>

<style scoped>
.runtime-item {
  @apply rounded-xl border border-gray-100 bg-gray-50/80 px-3 py-2.5 dark:border-dark-700/60 dark:bg-dark-900/40;
}

.status-pill {
  @apply inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset;
}
</style>
