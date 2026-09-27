import '@/styles/onboarding.css'
import { defineComponent, onMounted } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useOnboardingTour } from '@/composables/useOnboardingTour'
import { useOnboardingStore } from '@/stores/onboarding'

/** 新手引导宿主：随后台布局挂载一次，路由切换时保持存活；不渲染任何内容 */
export default defineComponent({
  name: 'AppOnboardingTour',
  setup() {
    const authStore = useAuthStore()
    const onboardingStore = useOnboardingStore()

    const { replayTour } = useOnboardingTour({
      storageKey: authStore.user?.role === 'admin' ? 'admin_guide' : 'user_guide',
      autoStart: true
    })

    onMounted(() => {
      onboardingStore.setReplayCallback(replayTour)
    })

    return () => null
  }
})
