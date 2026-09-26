<template>
  <img
    v-if="src && !failed"
    :src="src"
    :alt="label || normalizedCode"
    :title="label || undefined"
    class="h-3 w-4 flex-shrink-0 rounded-[2px] object-cover ring-1 ring-black/5 dark:ring-white/10"
    loading="lazy"
    @error="failed = true"
  />
  <span
    v-else-if="normalizedCode"
    class="inline-flex h-3.5 flex-shrink-0 items-center rounded-[3px] bg-gray-100 px-1 text-[9px] font-semibold leading-none text-gray-500 dark:bg-dark-700 dark:text-dark-300"
    :title="label || undefined"
  >{{ normalizedCode }}</span>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { countryFlagUrl } from '@/utils/clash'

const props = defineProps<{
  code?: string | null
  label?: string | null
}>()

const failed = ref(false)
const normalizedCode = computed(() => (props.code || '').trim().toUpperCase())
const src = computed(() => countryFlagUrl(props.code))

watch(() => props.code, () => {
  failed.value = false
})
</script>
