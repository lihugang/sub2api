<template>
  <div class="mt-3 flex items-start justify-between gap-3">
    <div class="min-w-0">
      <p class="text-xs font-medium text-gray-700 dark:text-gray-200">
        {{ t('admin.accounts.manualPriorityProtection') }}
      </p>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.manualPriorityProtectionHint') }}
      </p>
    </div>
    <Toggle
      :model-value="modelValue"
      data-testid="manual-priority-protection"
      :aria-label="t('admin.accounts.manualPriorityProtection')"
      @update:model-value="emit('update:modelValue', $event)"
    />
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'

defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()
const { t } = useI18n()
</script>
