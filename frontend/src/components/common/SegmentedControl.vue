<template>
  <div
    class="segmented"
    :class="size === 'sm' ? 'segmented-sm' : ''"
    role="radiogroup"
    :aria-label="ariaLabel"
    @keydown="onKeydown"
  >
    <button
      v-for="(option, index) in options"
      :key="String(option.value)"
      ref="buttonRefs"
      type="button"
      role="radio"
      class="segmented-item"
      :class="{ 'segmented-item-active': option.value === modelValue }"
      :aria-checked="option.value === modelValue"
      :tabindex="option.value === modelValue || (activeIndex === -1 && index === 0) ? 0 : -1"
      :title="option.title"
      :aria-label="option.label ? undefined : option.title"
      :data-testid="testId ? `${testId}-${String(option.value)}` : undefined"
      @click="select(option.value)"
    >
      <Icon v-if="option.icon" :name="option.icon" size="sm" :stroke-width="1.75" />
      <span v-if="option.label">{{ option.label }}</span>
      <span
        v-if="option.count !== undefined"
        class="segmented-count"
      >{{ option.count }}</span>
    </button>
  </div>
</template>

<script setup lang="ts" generic="T extends string | number | boolean | null">
import { computed, ref } from 'vue'
import Icon from '@/components/icons/Icon.vue'

type IconName = InstanceType<typeof Icon>['$props']['name']

interface SegmentedOption<V> {
  value: V
  label?: string
  icon?: IconName
  title?: string
  count?: number | string
}

const props = withDefaults(
  defineProps<{
    modelValue: T
    options: SegmentedOption<T>[]
    size?: 'sm' | 'md'
    ariaLabel?: string
    testId?: string
  }>(),
  {
    size: 'md',
    ariaLabel: undefined,
    testId: undefined
  }
)

const emit = defineEmits<{
  'update:modelValue': [value: T]
  change: [value: T]
}>()

const buttonRefs = ref<HTMLButtonElement[]>([])
const activeIndex = computed(() => props.options.findIndex((o) => o.value === props.modelValue))

function select(value: T) {
  if (value === props.modelValue) return
  emit('update:modelValue', value)
  emit('change', value)
}

// 方向键在选项间移动（radiogroup 的标准键盘行为）
function onKeydown(event: KeyboardEvent) {
  if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return
  event.preventDefault()
  const count = props.options.length
  if (!count) return
  const step = event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : 1
  const current = activeIndex.value === -1 ? 0 : activeIndex.value
  const next = (current + step + count) % count
  select(props.options[next].value)
  buttonRefs.value[next]?.focus()
}
</script>
