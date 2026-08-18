<template>
  <section class="border-t pt-4" aria-labelledby="time-rate-rules-title">
    <div class="mb-3 flex items-start justify-between gap-3">
      <div>
        <h4 id="time-rate-rules-title" class="text-sm font-medium text-gray-800 dark:text-gray-100">
          {{ t('admin.groups.timeRate.title') }}
        </h4>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.groups.timeRate.hint') }}
        </p>
      </div>
      <span class="shrink-0 border border-cyan-200 bg-cyan-50 px-2 py-1 text-xs font-medium text-cyan-800 dark:border-cyan-800 dark:bg-cyan-950/40 dark:text-cyan-200">
        UTC+08:00
      </span>
    </div>

    <div v-if="model.length" class="space-y-2">
      <div v-for="(rule, index) in model" :key="index" class="grid grid-cols-1 items-end gap-2 border border-gray-200 bg-gray-50 p-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_32px] dark:border-dark-600 dark:bg-dark-800">
        <label class="block">
          <span class="input-label">{{ t('admin.groups.timeRate.start') }}</span>
          <input v-model="rule.start" type="time" class="input" />
        </label>
        <label class="block">
          <span class="input-label">{{ t('admin.groups.timeRate.end') }}</span>
          <input v-model="rule.end" type="text" inputmode="numeric" class="input" placeholder="18:00 / 24:00" />
        </label>
        <label class="block">
          <span class="input-label">{{ t('admin.groups.timeRate.multiplier') }}</span>
          <input v-model.number="rule.multiplier" type="number" min="0" step="0.001" class="input" />
        </label>
        <button type="button" class="btn btn-secondary !h-9 !w-8 justify-self-end !px-0" :title="t('admin.groups.timeRate.remove')" @click="remove(index)">
          <Icon name="trash" size="sm" />
        </button>
      </div>
    </div>
    <p v-else class="border border-dashed border-gray-300 px-3 py-2 text-xs text-gray-500 dark:border-dark-600 dark:text-gray-400">
      {{ t('admin.groups.timeRate.empty') }}
    </p>

    <p v-if="validationError" class="mt-2 text-xs text-red-600 dark:text-red-400">{{ validationError }}</p>
    <button type="button" class="btn btn-secondary mt-3" @click="add">
      <Icon name="plus" size="sm" class="mr-1" />{{ t('admin.groups.timeRate.add') }}
    </button>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { TimeRateRule } from '@/types'

const model = defineModel<TimeRateRule[]>({ required: true })
const { t } = useI18n()

const minute = (value: string, allow24 = false): number | null => {
  if (allow24 && value === '24:00') return 1440
  const match = /^(\d{2}):(\d{2})$/.exec(value)
  if (!match) return null
  const hours = Number(match[1])
  const minutes = Number(match[2])
  return hours <= 23 && minutes <= 59 ? hours * 60 + minutes : null
}

const validationError = computed(() => {
  const ranges = model.value.map((rule, index) => ({ index, start: minute(rule.start), end: minute(rule.end, true), multiplier: rule.multiplier }))
  if (ranges.some((rule) => rule.start === null || rule.end === null || rule.start! >= rule.end! || !Number.isFinite(rule.multiplier) || rule.multiplier < 0)) return t('admin.groups.timeRate.invalid')
  ranges.sort((left, right) => left.start! - right.start!)
  return ranges.some((rule, index) => index > 0 && rule.start! < ranges[index - 1].end!) ? t('admin.groups.timeRate.overlap') : ''
})

const add = () => model.value.push({ start: '09:00', end: '18:00', multiplier: 1 })
const remove = (index: number) => model.value.splice(index, 1)
</script>
