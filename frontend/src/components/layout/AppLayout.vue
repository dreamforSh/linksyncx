<template>
  <div v-if="shell" class="app-page">
    <slot />
  </div>
  <AppShell v-else standalone>
    <div class="app-page">
      <slot />
    </div>
  </AppShell>
</template>

<script setup lang="ts">
import { inject, onBeforeUnmount } from 'vue'
import AppShell from './AppShell.vue'
import { APP_SHELL_KEY } from './appShellContext'

// 外层有常驻 AppShell（App.vue）时只登记并渲染页面内容，侧边栏/顶栏跨路由保持挂载；
// 没有常驻壳（如单独挂载组件）时自带完整布局。
const shell = inject(APP_SHELL_KEY, null)
if (shell) {
  shell.register()
  onBeforeUnmount(shell.unregister)
}
</script>

<style scoped>
/* 页面进入：轻微淡入上浮。不保留终态 transform，避免给页面内的 fixed 元素创建包含块 */
.app-page {
  animation: app-page-enter 220ms cubic-bezier(0.2, 0, 0, 1);
}

@keyframes app-page-enter {
  from {
    opacity: 0;
    transform: translateY(6px);
  }
}

@media (prefers-reduced-motion: reduce) {
  .app-page {
    animation: none;
  }
}
</style>
