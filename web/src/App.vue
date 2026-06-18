<script setup>
import { ref, reactive, computed, nextTick, onMounted } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { getProfiles, getModels, getHistory, chatStream } from './api.js'

marked.setOptions({ breaks: true, gfm: true })

// md renders assistant markdown to sanitized HTML. Output comes from an LLM and
// is injected with v-html, so sanitizing is mandatory (no scripts/handlers).
function md(text) {
  return DOMPurify.sanitize(marked.parse(text || ''))
}

const conn = reactive({
  base: localStorage.getItem('harness.base') || 'http://localhost:8080',
  token: localStorage.getItem('harness.token') || '',
  connected: false,
  error: '',
})

const profiles = ref([])           // [{name, description}]
const providers = ref([])          // [{provider, default, models:[{label,id}]}]
const profile = ref('')
const providerSlug = ref('')
const model = ref('')              // '' = provider default
const sessionId = ref('web')
const messages = reactive([])      // {role:'user'|'assistant'|'note', text}
const input = ref('')
const streaming = ref(false)
const scroller = ref(null)

const modelsForProvider = computed(() => {
  const p = providers.value.find((x) => x.provider === providerSlug.value)
  return p ? p.models : []
})

async function connect() {
  conn.error = ''
  try {
    const [pr, md] = await Promise.all([
      getProfiles(conn.base, conn.token),
      getModels(conn.base, conn.token),
    ])
    profiles.value = pr.profiles || []
    providers.value = md.providers || []
    profile.value = pr.default || (profiles.value[0] && profiles.value[0].name) || ''
    providerSlug.value = providers.value[0] ? providers.value[0].provider : ''
    localStorage.setItem('harness.base', conn.base)
    localStorage.setItem('harness.token', conn.token)
    conn.connected = true
    await loadHistory()
  } catch (e) {
    conn.error = String(e.message || e)
  }
}

function disconnect() {
  conn.connected = false
}

async function loadHistory() {
  messages.length = 0
  try {
    const h = await getHistory(conn.base, conn.token, profile.value, sessionId.value)
    for (const m of h.messages || []) {
      const text = (m.Content || [])
        .filter((b) => b.Type === 'text')
        .map((b) => b.Text)
        .join('')
      if (text.trim()) messages.push({ role: m.Role === 'user' ? 'user' : 'assistant', text })
    }
    if (h.provider) providerSlug.value = h.provider
    if (h.model) model.value = h.model
    scrollDown()
  } catch {
    // a fresh session has no history yet — fine
  }
}

async function send() {
  const text = input.value.trim()
  if (!text || streaming.value) return
  input.value = ''
  messages.push({ role: 'user', text })
  const reply = reactive({ role: 'assistant', text: '' })
  messages.push(reply)
  streaming.value = true
  scrollDown()

  try {
    await chatStream(
      conn.base,
      conn.token,
      {
        profile: profile.value,
        session: sessionId.value,
        message: text,
        provider: providerSlug.value,
        model: model.value,
      },
      (event, data) => {
        if (event === 'text') reply.text += data.delta || ''
        else if (event === 'tool_start') reply.text += `\n_⚙ ${data.name}…_\n`
        else if (event === 'error') reply.text += `\n[error: ${data.error}]`
        scrollDown()
      },
    )
  } catch (e) {
    reply.text += `\n[failed: ${e.message || e}]`
  } finally {
    streaming.value = false
    if (!reply.text) reply.text = '(no reply)'
    scrollDown()
  }
}

function scrollDown() {
  nextTick(() => {
    if (scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight
  })
}

function onKey(e) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    send()
  }
}

onMounted(() => {
  if (conn.token) connect()
})
</script>

<template>
  <div class="app">
    <header>
      <strong>Harness</strong>
      <span v-if="conn.connected" class="muted">· {{ profile }}</span>
      <span class="spacer"></span>
      <button v-if="conn.connected" class="ghost" @click="disconnect">settings</button>
    </header>

    <!-- Connect -->
    <section v-if="!conn.connected" class="connect">
      <h2>Connect</h2>
      <label>API URL<input v-model="conn.base" placeholder="http://localhost:8080" /></label>
      <label>Token<input v-model="conn.token" type="password" placeholder="bearer token" /></label>
      <button @click="connect">Connect</button>
      <p v-if="conn.error" class="err">{{ conn.error }}</p>
      <p class="muted small">Start the daemon: <code>harness serve -provider claude</code> — it prints a token.</p>
    </section>

    <!-- Chat -->
    <template v-else>
      <div class="bar">
        <select v-model="profile" @change="loadHistory" title="identity">
          <option v-for="p in profiles" :key="p.name" :value="p.name">{{ p.name }}</option>
        </select>
        <select v-model="providerSlug" title="provider">
          <option v-for="p in providers" :key="p.provider" :value="p.provider">{{ p.provider }}</option>
        </select>
        <select v-model="model" title="model">
          <option value="">default</option>
          <option v-for="m in modelsForProvider" :key="m.id" :value="m.id">{{ m.label }}</option>
        </select>
        <input class="session" v-model="sessionId" @change="loadHistory" title="session" />
      </div>

      <div class="messages" ref="scroller">
        <div v-for="(m, i) in messages" :key="i" :class="['msg', m.role]">
          <div class="who">{{ m.role === 'user' ? 'you' : 'morpheus' }}</div>
          <div v-if="m.role === 'user'" class="text">{{ m.text }}</div>
          <div v-else class="text md" v-html="md(m.text)"></div>
        </div>
        <p v-if="!messages.length" class="muted small">No messages yet — say hello.</p>
      </div>

      <div class="composer">
        <textarea
          v-model="input"
          @keydown="onKey"
          rows="1"
          :disabled="streaming"
          placeholder="Message… (Enter to send, Shift+Enter for newline)"
        ></textarea>
        <button @click="send" :disabled="streaming || !input.trim()">
          {{ streaming ? '…' : 'Send' }}
        </button>
      </div>
    </template>
  </div>
</template>
