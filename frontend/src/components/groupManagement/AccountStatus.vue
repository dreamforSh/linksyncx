<template>
  <StatusBadge :status="status" :label="label" />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import StatusBadge from '@/components/common/StatusBadge.vue'

const props = defineProps<{ status: string }>()

const { t, te } = useI18n()

const label = computed(() => {
  const key = `groupManagement.accounts.statuses.${props.status}`
  return te(key) ? t(key) : props.status
})
</script>
