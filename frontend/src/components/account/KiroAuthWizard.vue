<template>
  <div class="rounded-lg border border-blue-200 bg-blue-50 p-4 dark:border-blue-700 dark:bg-blue-900/30">
    <div class="flex items-start gap-3">
      <div class="flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-lg bg-blue-500">
        <Icon name="link" size="md" class="text-white" />
      </div>
      <div class="min-w-0 flex-1">
        <h4 class="mb-2 font-semibold text-blue-900 dark:text-blue-200">
          {{ mode === 'idc' ? 'IAM Identity Center 授权码授权' : 'AWS Builder ID 设备码授权' }}
        </h4>

        <!-- idle：尚未发起授权 -->
        <template v-if="phase === 'idle'">
          <p class="mb-3 text-xs text-blue-800 dark:text-blue-300">
            <template v-if="mode === 'idc'">
              点击后会在新标签页打开 AWS SSO 登录页，请用组织账号登录并完成授权。
            </template>
            <template v-else> 点击后生成一次性设备码，需要在弹出的页面用 AWS Builder ID 账号登录并批准。 </template>
          </p>
          <button
            type="button"
            data-test="kiro-wizard-start"
            class="btn btn-primary btn-sm"
            :disabled="loading || (mode === 'idc' && !issuerUrlTrimmed)"
            @click="handleStart"
          >
            {{ loading ? '正在获取…' : mode === 'idc' ? '生成授权链接' : '获取设备码' }}
          </button>
          <p v-if="mode === 'idc' && !issuerUrlTrimmed" class="mt-1 text-[11px] text-amber-600 dark:text-amber-400">
            请先在下方填写 SSO 门户地址
          </p>
        </template>

        <!-- started: idc -->
        <template v-else-if="phase === 'started' && mode === 'idc'">
          <p class="mb-2 text-xs text-blue-800 dark:text-blue-300">
            已在新标签页打开授权链接。若被浏览器拦截，
            <button type="button" class="underline" @click="reopenIdcWindow">点此重新打开</button>。
          </p>
          <p class="mb-2 text-xs text-blue-800 dark:text-blue-300">
            登录并同意授权后，浏览器会跳转到一个<strong>打不开</strong>的地址（显示"无法连接"，这是正常的——
            该地址本来就没有服务监听）。请把<strong>地址栏里的完整 URL</strong>复制下来，粘贴到下方后点击提交。
          </p>
          <p class="mb-2 text-[11px]" :class="isExpired ? 'text-red-600 dark:text-red-400' : 'text-blue-600 dark:text-blue-400'">
            <template v-if="!isExpired">授权链接 {{ remainingSeconds }} 秒后失效</template>
            <template v-else>授权会话已过期，请重新生成授权链接</template>
          </p>
          <textarea
            v-model="callbackUrlInput"
            data-test="kiro-wizard-callback-url"
            rows="2"
            class="input mb-2 w-full font-mono text-xs"
            placeholder="http://127.0.0.1/oauth/callback?code=...&amp;state=..."
            :disabled="isExpired"
          ></textarea>
          <div class="flex gap-2">
            <button
              type="button"
              data-test="kiro-wizard-confirm"
              class="btn btn-primary btn-sm"
              :disabled="loading || isExpired || !callbackUrlInput.trim()"
              @click="confirmIdcDone"
            >
              {{ loading ? '提交中…' : '提交回调地址' }}
            </button>
            <button type="button" class="btn btn-secondary btn-sm" @click="resetIdc">重新开始</button>
          </div>
        </template>

        <!-- started: builder_id -->
        <template v-else-if="phase === 'started' && mode === 'builder_id'">
          <div class="mb-2 flex items-center gap-2">
            <span
              data-test="kiro-wizard-user-code"
              class="rounded bg-white px-3 py-1.5 font-mono text-lg font-semibold tracking-widest text-blue-900 dark:bg-dark-800 dark:text-blue-100"
            >
              {{ userCode }}
            </span>
            <button type="button" class="text-xs text-blue-600 hover:underline dark:text-blue-400" @click="copyUserCode">
              {{ copied ? '已复制' : '复制' }}
            </button>
          </div>
          <p class="mb-2 text-xs text-blue-800 dark:text-blue-300">
            已在新标签页打开验证页面并预填此码。若未自动打开，
            <a :href="verificationUriComplete" target="_blank" rel="noopener noreferrer" class="underline">点此打开</a>。
          </p>
          <p class="mb-2 text-[11px]" :class="isExpired ? 'text-red-600 dark:text-red-400' : 'text-blue-600 dark:text-blue-400'">
            <template v-if="!isExpired">设备码 {{ remainingSeconds }} 秒后失效，正在自动轮询授权结果…</template>
            <template v-else>设备码已过期，请重新获取</template>
          </p>
          <button type="button" class="btn btn-secondary btn-sm" @click="resetDevice">重新获取</button>
        </template>

        <!-- success -->
        <template v-else-if="phase === 'success'">
          <p data-test="kiro-wizard-success" class="text-xs font-medium text-green-700 dark:text-green-400">
            已获取凭证，下方字段已自动填入。如需更换账号可重新授权。
          </p>
          <button type="button" class="btn btn-secondary btn-sm mt-2" @click="mode === 'idc' ? resetIdc() : resetDevice()">
            重新授权
          </button>
        </template>

        <p v-if="error" data-test="kiro-wizard-error" class="mt-2 text-xs text-red-600 dark:text-red-400">
          {{ error }}
        </p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { adminAPI } from '@/api/admin'
import { extractApiErrorMessage } from '@/utils/apiError'
import Icon from '@/components/icons/Icon.vue'
import type { KiroCredentialForm } from './kiroCredentials'

interface Props {
  /** 与 KiroCredentialFields 当前选中的 authMethod 对应，决定展示哪条流程。 */
  mode: 'idc' | 'builder_id'
  issuerUrl?: string
  region?: string
  proxyId?: number | null
}

const props = defineProps<Props>()
const emit = defineEmits<{
  /** 授权成功后把可回填的字段交给父组件合并进 KiroCredentialForm。 */
  filled: [value: Partial<KiroCredentialForm>]
}>()

type Phase = 'idle' | 'started' | 'success'

const phase = ref<Phase>('idle')
const loading = ref(false)
const error = ref('')
const sessionId = ref('')
const expiresAt = ref<number | null>(null)
const nowTick = ref(Date.now())

// IdC 专用
const authorizeUrlValue = ref('')
const callbackUrlInput = ref('')
// Builder ID（设备码）专用
const userCode = ref('')
const verificationUriComplete = ref('')
const copied = ref(false)

let pollTimer: ReturnType<typeof setTimeout> | null = null
let tickTimer: ReturnType<typeof setInterval> | null = null

const issuerUrlTrimmed = computed(() => (props.issuerUrl || '').trim())

const isExpired = computed(() => expiresAt.value != null && nowTick.value >= expiresAt.value)
const remainingSeconds = computed(() => {
  if (expiresAt.value == null) return 0
  return Math.max(0, Math.round((expiresAt.value - nowTick.value) / 1000))
})

function resolvedRegion(): string {
  return (props.region || '').trim() || 'us-east-1'
}

function clearPollTimer() {
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
}

function startTicking() {
  if (tickTimer) return
  tickTimer = setInterval(() => {
    nowTick.value = Date.now()
  }, 1000)
}

function stopTicking() {
  if (tickTimer) {
    clearInterval(tickTimer)
    tickTimer = null
  }
}

onBeforeUnmount(() => {
  clearPollTimer()
  stopTicking()
})

// 切换 idc/builder_id 时清空上一条流程的会话状态，避免串号。
watch(
  () => props.mode,
  () => {
    resetIdc()
    resetDevice()
  }
)

function handleStart() {
  if (props.mode === 'idc') {
    void startIdc()
  } else {
    void startDeviceAuth()
  }
}

// ---------- IdC（授权码）----------

async function startIdc() {
  const issuer = issuerUrlTrimmed.value
  if (!issuer) {
    error.value = '请先填写 SSO 门户地址'
    return
  }
  loading.value = true
  error.value = ''
  try {
    const res = await adminAPI.kiro.authorizeUrl({
      issuer_url: issuer,
      region: resolvedRegion(),
      proxy_id: props.proxyId ?? undefined
    })
    sessionId.value = res.session_id
    authorizeUrlValue.value = res.authorize_url
    expiresAt.value = Date.now() + res.expires_in * 1000
    phase.value = 'started'
    startTicking()
    openWindow(res.authorize_url)
  } catch (err) {
    error.value = extractApiErrorMessage(err, '生成授权链接失败')
  } finally {
    loading.value = false
  }
}

function reopenIdcWindow() {
  if (authorizeUrlValue.value) openWindow(authorizeUrlValue.value)
}

async function confirmIdcDone() {
  if (isExpired.value) {
    error.value = '授权会话已过期，请重新生成授权链接'
    return
  }
  const callbackUrl = callbackUrlInput.value.trim()
  if (!callbackUrl) {
    error.value = '请粘贴授权后浏览器地址栏的完整 URL'
    return
  }
  loading.value = true
  error.value = ''
  try {
    const res = await adminAPI.kiro.completeIdC({
      session_id: sessionId.value,
      callback_url: callbackUrl,
      proxy_id: props.proxyId ?? undefined
    })
    if (res.status === 'ok' && res.credentials) {
      applyCredentials(res.credentials)
    } else {
      error.value = '未能从粘贴的地址里解析出授权结果，请确认复制的是完整地址栏 URL'
    }
  } catch (err) {
    error.value = extractApiErrorMessage(err, '完成授权失败')
  } finally {
    loading.value = false
  }
}

function resetIdc() {
  if (phase.value !== 'idle') phase.value = 'idle'
  authorizeUrlValue.value = ''
  callbackUrlInput.value = ''
  sessionId.value = ''
  expiresAt.value = null
  error.value = ''
  stopTicking()
}

// ---------- Builder ID（设备码）----------

async function startDeviceAuth() {
  loading.value = true
  error.value = ''
  try {
    const res = await adminAPI.kiro.deviceStart({
      region: resolvedRegion(),
      proxy_id: props.proxyId ?? undefined
    })
    sessionId.value = res.session_id
    userCode.value = res.user_code
    verificationUriComplete.value = res.verification_uri_complete
    expiresAt.value = Date.now() + res.expires_in * 1000
    phase.value = 'started'
    startTicking()
    openWindow(res.verification_uri_complete)
    schedulePoll(res.interval * 1000)
  } catch (err) {
    error.value = extractApiErrorMessage(err, '获取设备码失败')
  } finally {
    loading.value = false
  }
}

// 轮询必须按后端返回的 interval 节流，且在 expires_in 到期后停止——
// 后端对 slow_down 有专门处理，前端抢跑会触发它（见 brief Step 4）。
function schedulePoll(delayMs: number) {
  clearPollTimer()
  pollTimer = setTimeout(() => {
    void runPoll()
  }, Math.max(1000, delayMs))
}

async function runPoll() {
  if (phase.value !== 'started') return
  if (isExpired.value) {
    error.value = '设备码已过期，请重新获取'
    stopTicking()
    return
  }
  try {
    const res = await adminAPI.kiro.devicePoll({
      session_id: sessionId.value,
      proxy_id: props.proxyId ?? undefined
    })
    if (res.status === 'ok' && res.credentials) {
      applyCredentials(res.credentials)
      return
    }
    // pending：按响应里的 interval 安排下一次轮询（后端 slow_down 时会调大它）。
    const nextIntervalSeconds = res.interval && res.interval > 0 ? res.interval : 5
    schedulePoll(nextIntervalSeconds * 1000)
  } catch (err) {
    error.value = extractApiErrorMessage(err, '设备码授权失败')
    stopTicking()
  }
}

function resetDevice() {
  if (phase.value !== 'idle') phase.value = 'idle'
  clearPollTimer()
  stopTicking()
  userCode.value = ''
  verificationUriComplete.value = ''
  sessionId.value = ''
  expiresAt.value = null
  error.value = ''
}

// ---------- 共用 ----------

function openWindow(url: string) {
  if (typeof window === 'undefined') return
  window.open(url, '_blank', 'noopener,noreferrer')
}

function applyCredentials(raw: Record<string, unknown>) {
  clearPollTimer()
  stopTicking()
  phase.value = 'success'
  const str = (k: string): string => (typeof raw[k] === 'string' ? (raw[k] as string) : '')
  emit('filled', {
    refreshToken: str('refresh_token'),
    accessToken: str('access_token'),
    clientId: str('client_id'),
    clientSecret: str('client_secret'),
    issuerUrl: str('issuer_url'),
    region: str('region'),
    profileArn: str('profile_arn')
  })
}

async function copyUserCode() {
  try {
    await navigator.clipboard.writeText(userCode.value)
    copied.value = true
    setTimeout(() => {
      copied.value = false
    }, 1500)
  } catch {
    // clipboard 不可用（如非安全上下文）时静默失败，用户仍可手动选中复制。
  }
}
</script>
