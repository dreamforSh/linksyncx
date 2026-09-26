<template>
  <span
    class="inline-flex flex-shrink-0 items-center rounded px-1.5 py-px align-middle text-[10px] font-semibold leading-4 ring-1 ring-inset bg-indigo-50 text-indigo-700 ring-indigo-600/20 dark:bg-indigo-500/15 dark:text-indigo-300 dark:ring-indigo-400/30"
    data-testid="clash-tag"
  >{{ t('admin.clash.common.tag') }}</span>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
</script>
