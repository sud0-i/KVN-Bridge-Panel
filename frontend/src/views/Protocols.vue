<script setup>
import { computed } from 'vue'
import Icon from '../ui/Icon.vue'
import Toggle from '../ui/Toggle.vue'
import Seg from '../ui/Seg.vue'
import SaveBar from '../ui/SaveBar.vue'
import { t } from '../i18n'
import { routing, fingerprints, hasExits, preview, saveSettings, nodes } from '../store'

const fpOptions = computed(() => fingerprints.value.map(v => ({ value: v, label: v })))
const linkOptions = computed(() => [
  { value: 'tcp', label: 'TCP (Vision)' },
  { value: 'xhttp', label: 'XHTTP', disabled: !routing.value?.xhttp },
  { value: 'cdn', label: t('pr.linkCDN'), disabled: !routing.value?.cdn },
])
const kindLabel = { vless: 'VLESS', xhttp: 'XHTTP', hy2: 'Hysteria2', cdn: 'CDN', mieru: 'mieru' }
const mieruNodes = computed(() => nodes.value.filter(n => n.MieruVersion))
const mieruErrors = computed(() => nodes.value.filter(n => n.MieruError))
const cdnNodes = computed(() => nodes.value.filter(n => n.CDNDomain))
</script>

<template>
  <div v-if="!routing" class="text-mute">{{ t('loading') }}</div>
  <div v-else class="flex flex-col gap-4 sm:gap-6">
    <div class="flex flex-col xl:flex-row gap-4 sm:gap-6 xl:items-start">
      <div class="flex flex-col gap-4 flex-1 min-w-0">
        <!-- VLESS -->
        <section class="card">
          <div class="flex items-start gap-4">
            <div class="flex flex-col gap-1.5 flex-1">
              <div class="flex flex-wrap items-center gap-2"><h2 class="text-base font-semibold">VLESS + Reality</h2><span class="pill bg-surf2 text-mute">TCP 443</span></div>
              <p class="text-[13px] text-mute leading-relaxed">{{ t('pr.vless') }}</p>
            </div>
            <span class="pill bg-surf2 text-mute">{{ t('pr.always') }}</span>
          </div>
          <div class="border-t border-line pt-4 flex flex-col gap-2">
            <span class="text-[13px] text-mute">{{ t('fingerprint') }}</span>
            <Seg v-model="routing.fingerprint" :options="fpOptions" :label="t('fingerprint')" />
            <p class="hint">{{ t('fingerprintHint') }}</p>
          </div>
        </section>

        <!-- XHTTP -->
        <section class="card">
          <div class="flex items-start gap-4">
            <div class="flex flex-col gap-1.5 flex-1">
              <div class="flex flex-wrap items-center gap-2"><h2 class="text-base font-semibold">XHTTP</h2><span class="pill bg-surf2 text-mute">TCP 443</span></div>
              <p class="text-[13px] text-mute leading-relaxed">{{ t('xhttpHint') }}</p>
            </div>
            <Toggle v-model="routing.xhttp" label="XHTTP" />
          </div>
        </section>

        <!-- Hysteria2 -->
        <section class="card">
          <div class="flex items-start gap-4">
            <div class="flex flex-col gap-1.5 flex-1">
              <div class="flex flex-wrap items-center gap-2"><h2 class="text-base font-semibold">Hysteria2</h2><span class="pill bg-surf2 text-mute">UDP 443</span></div>
              <p class="text-[13px] text-mute leading-relaxed">{{ t('hysteriaHint') }}</p>
            </div>
            <Toggle v-model="routing.hysteria" label="Hysteria2" />
          </div>
          <div v-if="routing.hysteria" class="border-t border-line pt-4 flex items-start gap-4">
            <div class="flex flex-col gap-1.5 flex-1">
              <span class="text-sm font-medium">{{ t('pr.salamander') }}</span>
              <span class="text-[13px] text-mute leading-relaxed">{{ t('hysteriaObfsHint') }}</span>
            </div>
            <Toggle v-model="routing.hysteria_obfs" :label="t('pr.salamander')" />
          </div>
        </section>

        <!-- mieru -->
        <section class="card">
          <div class="flex items-start gap-4">
            <div class="flex flex-col gap-1.5 flex-1">
              <div class="flex flex-wrap items-center gap-2"><h2 class="text-base font-semibold">mieru</h2><span class="pill bg-surf2 text-mute">TCP {{ routing.mieru_ports || '40100-40109' }}</span></div>
              <p class="text-[13px] text-mute leading-relaxed">{{ t('pr.mieruHint') }}</p>
            </div>
            <Toggle v-model="routing.mieru" label="mieru" />
          </div>
          <div v-if="routing.mieru" class="border-t border-line pt-4 flex flex-col gap-3">
            <div>
              <label class="label" for="mieru-ports">{{ t('pr.mieruPorts') }}</label>
              <input id="mieru-ports" v-model.trim="routing.mieru_ports" placeholder="40100-40109" class="input w-48 font-mono">
              <p class="hint mt-1.5">{{ t('pr.mieruPortsHint') }}</p>
            </div>
            <p v-if="mieruNodes.length" class="text-[13px] text-mute">{{ t('pr.mieruRunning') }} <span class="text-fg">{{ mieruNodes.map(n => n.Label || n.IP).join(', ') }}</span></p>
            <p v-else class="text-[13px] text-warn">{{ t('pr.mieruWaiting') }}</p>
            <p v-for="n in mieruErrors" :key="n.IP" class="text-[13px] text-err break-words">{{ n.Label || n.IP }}: {{ n.MieruError }}</p>
          </div>
        </section>

        <!-- Через CDN -->
        <section class="card">
          <div class="flex items-start gap-4">
            <div class="flex flex-col gap-1.5 flex-1">
              <div class="flex flex-wrap items-center gap-2"><h2 class="text-base font-semibold">{{ t('pr.cdn') }}</h2><span class="pill bg-surf2 text-mute">TCP 8443</span></div>
              <p class="text-[13px] text-mute leading-relaxed">{{ t('pr.cdnHint') }}</p>
            </div>
            <Toggle v-model="routing.cdn" :label="t('pr.cdn')" />
          </div>
          <div v-if="routing.cdn" class="border-t border-line pt-4 flex flex-col gap-3 text-[13px]">
            <p class="text-mute">{{ t('pr.cdnSetup') }}</p>
            <ol class="list-decimal ml-5 flex flex-col gap-1.5 text-mute leading-relaxed">
              <li>{{ t('pr.cdnStep1') }}</li>
              <li>{{ t('pr.cdnStep2') }}</li>
              <li>{{ t('pr.cdnStep3') }}</li>
              <li>{{ t('pr.cdnStep4') }}</li>
            </ol>
            <p v-if="!cdnNodes.length" class="text-warn">{{ t('pr.cdnNoNodes') }}</p>
            <p v-else class="text-mute">{{ t('pr.cdnNodes') }} <span class="font-mono text-fg">{{ cdnNodes.map(n => n.CDNDomain).join(', ') }}</span></p>
          </div>
        </section>

        <!-- Связь моста с экзитами -->
        <section v-if="hasExits" class="card">
          <div class="flex flex-col gap-1.5">
            <h2 class="text-base font-semibold">{{ t('exitLink') }}</h2>
            <p class="text-[13px] text-mute leading-relaxed">{{ t('exitLinkHint') }}</p>
          </div>
          <Seg v-model="routing.exit_link" :options="linkOptions" :label="t('exitLink')" />
        </section>

        <!-- Прямые ссылки -->
        <section v-if="hasExits" class="card">
          <div class="flex items-start gap-4">
            <div class="flex flex-col gap-1.5 flex-1">
              <h2 class="text-base font-semibold">{{ t('directLinks') }}</h2>
              <p class="text-[13px] text-mute leading-relaxed">{{ t('directLinksHint') }}</p>
            </div>
            <Toggle v-model="routing.direct_exit_links" :label="t('directLinks')" />
          </div>
        </section>
      </div>

      <!-- Что получит пользователь -->
      <section class="card xl:w-[380px] shrink-0 xl:sticky xl:top-8">
        <h2 class="h2">{{ t('pr.preview') }}</h2>
        <p class="text-[13px] text-mute leading-relaxed -mt-2">{{ t('pr.previewHint') }}</p>
        <p v-if="!preview.length" class="text-sm text-dim">{{ t('pr.previewEmpty') }}</p>
        <div v-else class="flex flex-col">
          <div class="flex items-center gap-3 py-2.5 border-t border-line">
            <span class="text-sm flex-1">{{ t('pr.auto') }}</span><span class="pill bg-surf2 text-mute">{{ t('pr.autoNote') }}</span>
          </div>
          <div v-for="s in preview" :key="s.name" class="flex items-center gap-3 py-2.5 border-t border-line">
            <span class="text-sm flex-1 min-w-0 truncate">{{ s.name }}</span>
            <span v-if="s.direct" class="text-xs text-dim">{{ t('pr.direct') }}</span>
            <span :class="['pill', s.kind === 'hy2' ? 'bg-acc-bg text-acc' : 'bg-surf2 text-mute']">{{ kindLabel[s.kind] }}</span>
          </div>
        </div>
        <p class="hint flex gap-2"><Icon name="link" :size="14" class="mt-0.5" />{{ t('pr.karing') }}</p>
      </section>
    </div>
    <SaveBar @save="saveSettings" />
  </div>
</template>
