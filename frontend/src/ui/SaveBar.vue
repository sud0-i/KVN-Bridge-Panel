<script setup>
// Кнопка «Сохранить» с результатом. На телефоне прилипает к низу экрана над вкладками.
import { saving, saveMessage, saveError } from '../store'
import { t } from '../i18n'
defineEmits(['save'])
</script>

<template>
  <div class="sticky bottom-[calc(64px+env(safe-area-inset-bottom))] lg:bottom-0 z-10 -mx-4 sm:-mx-6 lg:-mx-10 px-4 sm:px-6 lg:px-10 py-3 bg-ground/90 backdrop-blur border-t border-line flex flex-wrap items-center gap-3">
    <button type="button" class="btn btn-primary" :disabled="saving" @click="$emit('save')">{{ t('save') }}</button>
    <span v-if="saveMessage" class="text-ok text-sm">{{ saveMessage }}</span>
    <span v-if="saveError" class="text-err text-sm">{{ saveError }}</span>
  </div>
</template>
