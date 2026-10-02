<script setup>
import SaveBar from '../ui/SaveBar.vue'
import { t } from '../i18n'
import { computed } from 'vue'
import { routing, regions, warpTemplates, warpRulesText, bridgeDirectText, hasExits, addTemplate, saveSettings } from '../store'

// v-model на импортированный ref — через computed
const warpText = computed({ get: () => warpRulesText.value, set: (v) => { warpRulesText.value = v } })
const directText = computed({ get: () => bridgeDirectText.value, set: (v) => { bridgeDirectText.value = v } })
</script>

<template>
  <div v-if="!routing" class="text-mute">{{ t('loading') }}</div>
  <div v-else class="flex flex-col gap-4 sm:gap-6 max-w-3xl">
    <p v-if="!hasExits" class="text-sm text-acc bg-acc-bg border border-acc/30 rounded-lg p-3 leading-relaxed">{{ t('singleServerNote') }}</p>

    <section class="card">
      <h2 class="h2">{{ t('region') }}</h2>
      <select v-model="routing.region" class="input sm:w-72">
        <option value="">{{ t('regionNone') }}</option>
        <option v-for="(_, code) in regions" :key="code" :value="code">{{ t('regions.' + code) }}</option>
      </select>
      <p v-if="routing.region" class="text-xs text-dim font-mono -mt-2">{{ (regions[routing.region] || []).join(', ') }}</p>
      <fieldset v-if="routing.region" class="flex flex-col gap-3">
        <legend class="text-[13px] text-mute mb-3">{{ t('regionRoute') }}</legend>
        <label v-for="r in ['warp', 'bridge', 'exit']" :key="r" class="flex items-start gap-3 cursor-pointer text-sm leading-relaxed">
          <input type="radio" v-model="routing.region_route" :value="r" class="mt-1 accent-[#6AA8FF] w-4 h-4">
          <span>{{ t('route' + r.charAt(0).toUpperCase() + r.slice(1)) }}</span>
        </label>
      </fieldset>
      <p class="hint">{{ t('regionHint') }}</p>
    </section>

    <section class="card">
      <div class="flex flex-col gap-1.5">
        <h2 class="h2">{{ t('warpRules') }}</h2>
        <p class="text-[13px] text-mute leading-relaxed">{{ t('warpRulesHint') }}</p>
      </div>
      <div class="flex flex-wrap items-center gap-2 text-sm">
        <span class="text-dim">{{ t('templates') }}</span>
        <button v-for="(_, name) in warpTemplates" :key="name" type="button" @click="addTemplate(name)"
          class="h-8 px-3 rounded-lg border border-line text-mute hover:border-acc hover:text-acc transition-colors">+ {{ name }}</button>
      </div>
      <textarea v-model="warpText" rows="5" class="textarea" :aria-label="t('warpRules')"></textarea>
      <p class="hint">{{ t('warpNote') }}</p>
    </section>

    <section class="card">
      <div class="flex flex-col gap-1.5">
        <h2 class="h2">{{ t('bridgeDirect') }}</h2>
        <p class="text-[13px] text-mute leading-relaxed">{{ t('bridgeDirectHint') }}</p>
      </div>
      <textarea v-model="directText" rows="4" class="textarea" :aria-label="t('bridgeDirect')"></textarea>
    </section>

    <SaveBar @save="saveSettings" />
  </div>
</template>
