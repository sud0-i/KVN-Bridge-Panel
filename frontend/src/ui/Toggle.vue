<script setup>
const model = defineModel({ type: Boolean })
defineProps({ label: { type: String, required: true }, disabled: Boolean })
</script>

<template>
  <button type="button" role="switch" :aria-checked="model" :aria-label="label" :disabled="disabled" @click="model = !model"
    :class="['w-11 h-[26px] rounded-full p-[3px] flex shrink-0 transition-colors disabled:opacity-40', model ? 'bg-acc justify-end' : 'bg-surf2 border border-line justify-start']">
    <span :class="['w-5 h-5 rounded-full transition-colors', model ? 'bg-[#0B1220]' : 'bg-dim']"></span>
  </button>
</template>
