// API client for the harness /v1 endpoints. The chat endpoint is a POST that
// streams Server-Sent Events, so we use fetch + a ReadableStream (EventSource
// can't POST or set an Authorization header) and parse the SSE frames ourselves.

function trimBase(base) {
  return base.replace(/\/+$/, '')
}

async function getJSON(base, token, path) {
  const resp = await fetch(trimBase(base) + path, {
    headers: { Authorization: 'Bearer ' + token },
  })
  if (!resp.ok) throw new Error('HTTP ' + resp.status + ': ' + (await resp.text()))
  return resp.json()
}

export const getProfiles = (base, token) => getJSON(base, token, '/v1/profiles')
export const getModels = (base, token) => getJSON(base, token, '/v1/models')
export const getSessions = (base, token, profile) =>
  getJSON(base, token, '/v1/sessions?profile=' + encodeURIComponent(profile))
export const getHistory = (base, token, profile, id) =>
  getJSON(base, token, '/v1/sessions/' + encodeURIComponent(id) + '?profile=' + encodeURIComponent(profile))

// Background tasks: list the queue, inspect one, enqueue one.
export const getTasks = (base, token) => getJSON(base, token, '/v1/tasks')
export const getTask = (base, token, id) => getJSON(base, token, '/v1/tasks/' + encodeURIComponent(id))
export async function addTask(base, token, body) {
  const resp = await fetch(trimBase(base) + '/v1/tasks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + token },
    body: JSON.stringify(body),
  })
  if (!resp.ok) throw new Error('HTTP ' + resp.status + ': ' + (await resp.text()))
  return resp.json()
}

// chatStream POSTs a turn and invokes onEvent(type, data) for each SSE frame
// (ready, text, tool_start, tool_result, route, usage, stop, error, done).
export async function chatStream(base, token, body, onEvent, signal) {
  const resp = await fetch(trimBase(base) + '/v1/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + token },
    body: JSON.stringify(body),
    signal,
  })
  if (!resp.ok) throw new Error('HTTP ' + resp.status + ': ' + (await resp.text()))

  const reader = resp.body.getReader()
  const dec = new TextDecoder()
  let buf = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += dec.decode(value, { stream: true })
    let i
    while ((i = buf.indexOf('\n\n')) >= 0) {
      const frame = buf.slice(0, i)
      buf = buf.slice(i + 2)
      let event = 'message'
      let data = ''
      for (const line of frame.split('\n')) {
        if (line.startsWith('event:')) event = line.slice(6).trim()
        else if (line.startsWith('data:')) data += line.slice(5).trim()
      }
      if (!data) continue
      let parsed = data
      try {
        parsed = JSON.parse(data)
      } catch {
        // leave as string
      }
      onEvent(event, parsed)
    }
  }
}
