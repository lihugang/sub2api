<template>
  <section class="card" data-testid="profile-model-routing-notice-card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-medium text-gray-900 dark:text-white">
        {{ t('profile.modelRoutingNotice.title') }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('profile.modelRoutingNotice.description') }}
      </p>
    </div>

    <div class="flex flex-col gap-3 px-6 py-5 sm:flex-row sm:items-center sm:justify-between">
      <label for="model-routing-notice-mode" class="input-label mb-0">
        {{ t('profile.modelRoutingNotice.mode') }}
      </label>
      <div class="w-full sm:w-64">
        <Select
          id="model-routing-notice-mode"
          v-model="selectedMode"
          :options="options"
          :disabled="saving"
          :searchable="false"
          @change="saveMode"
        />
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import { userAPI } from '@/api'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { extractApiErrorMessage } from '@/utils/apiError'

type NoticeMode = 'disabled' | 'plain' | 'color'

const props = defineProps<{
  mode?: NoticeMode
}>()

const { t } = useI18n()
const authStore = useAuthStore()
const appStore = useAppStore()
const selectedMode = ref<NoticeMode>(props.mode ?? 'color')
const saving = ref(false)

const options = computed(() => [
  { value: 'disabled', label: t('profile.modelRoutingNotice.disabled') },
  { value: 'plain', label: t('profile.modelRoutingNotice.plain') },
  { value: 'color', label: t('profile.modelRoutingNotice.color') },
])

watch(() => props.mode, (mode) => {
  selectedMode.value = mode ?? 'color'
})

async function saveMode(value: string | number | boolean | null) {
  if (typeof value !== 'string' || !['disabled', 'plain', 'color'].includes(value)) {
    return
  }

  const previousMode = props.mode ?? 'color'
  const nextMode = value as NoticeMode
  if (nextMode === previousMode) {
    return
  }

  saving.value = true
  try {
    const updated = await userAPI.updateProfile({ model_routing_notice_mode: nextMode })
    authStore.user = updated
    appStore.showSuccess(t('common.saved'))
  } catch (error: unknown) {
    selectedMode.value = previousMode
    appStore.showError(extractApiErrorMessage(error, t('profile.modelRoutingNotice.saveFailed')))
  } finally {
    saving.value = false
  }
}
</script>
