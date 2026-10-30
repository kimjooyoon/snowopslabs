// SPDX-License-Identifier: Apache-2.0
import { useEffect, useState } from 'react'
import { api, ApiError } from '../api/client'
import { errMessage } from '../lib/errors'
import type { RunLogLine } from '../types'

/** How long to wait before reading again after a failed read. */
const RETRY_MS = 3000

/** useRunLogs tails a run's transcript by polling the cursor-forward logs
 *  endpoint and accumulating lines, exactly as `labctl runs logs --follow`
 *  does. Reading from a cursor (not a live socket) is what makes it correct:
 *  a reconnect or a slow render can never skip or duplicate a line (ADR-0006).
 *  Polling stops when the run is done or the server refuses the read; a
 *  dropped connection or a server error is shown and retried from the cursor. */
export function useRunLogs(id: string | null) {
  const [lines, setLines] = useState<RunLogLine[]>([])
  const [done, setDone] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    setLines([])
    setDone(false)
    setError(null)
    if (!id) return

    let cancelled = false
    let after = 0
    let timer: ReturnType<typeof setTimeout>

    const tick = async () => {
      try {
        const res = await api.getRunLogs(id, after)
        if (cancelled) return
        setError(null)
        if (res.lines.length > 0) {
          after = res.nextAfter
          setLines(prev => [...prev, ...res.lines])
        }
        if (res.done) {
          setDone(true)
          return
        }
        if (res.recoveryState === 'required' && res.lines.length === 0) {
          setError(`Recovery required. Check the lab, then use: ${res.recoveryCommand ?? `labctl runs recover ${id} --force`}`)
          return
        }
      } catch (e) {
        if (cancelled) return
        setError(errMessage(e))
        if (e instanceof ApiError && e.status < 500) return
        timer = setTimeout(tick, RETRY_MS)
        return
      }
      timer = setTimeout(tick, 1000)
    }
    void tick()

    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [id])

  return { lines, done, error }
}
