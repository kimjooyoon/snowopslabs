// SPDX-License-Identifier: Apache-2.0
//
// Runs view behaviour against the contract server (test/contract.ts): the
// list, the selected run's streamed output, and cancelling a live run.
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Runs } from './Runs'
import { page, serveContract } from '../test/contract'
import type { RunLogs, RunSummary } from '../types'

afterEach(() => {
  vi.unstubAllGlobals()
})

function renderRuns() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const notify = vi.fn()
  render(
    <QueryClientProvider client={client}>
      <Runs notify={notify} />
    </QueryClientProvider>,
  )
  return { notify }
}

const runningRun: RunSummary = {
  id: 'run_2', kind: 'lab.down', target: 'k3d', status: 'running',
  queuedAt: new Date().toISOString(), startedAt: new Date().toISOString(),
}
const noLogs = (status: RunSummary['status']): RunLogs => ({ runId: 'run_2', status, lines: [], nextAfter: 0, done: status !== 'running' })

describe('Runs view', () => {
  it('lists recorded runs with their status', async () => {
    const server = serveContract()
    renderRuns()
    expect(await screen.findByText('lab.up · k3d')).toBeInTheDocument()
    expect(screen.getAllByText('succeeded').length).toBeGreaterThan(0)
    expect(server.unanswered).toEqual([])
  })

  it('shows an empty state when there are no runs', async () => {
    const server = serveContract()
    server.override('GET', '/runs', 200, page([]))
    renderRuns()
    expect(await screen.findByText(/No runs recorded yet/)).toBeInTheDocument()
  })

  it('auto-selects the newest run and streams its output from the start', async () => {
    const server = serveContract()
    renderRuns()
    expect(await screen.findByText(/line two/)).toBeInTheDocument()
    const logReads = server.calls.filter(c => c.path === '/runs/run-1/logs')
    expect(logReads[0]?.query.get('after')).toBe('0')
  })

  it('offers a cancel control only for a live run and calls the API', async () => {
    const server = serveContract()
    server.override('GET', '/runs', 200, page([runningRun]))
    server.override('GET', '/runs/run_2', 200, { ...runningRun, steps: [] })
    server.override('GET', '/runs/run_2/logs', 200, noLogs('running'))
    server.override('POST', '/runs/run_2/cancel', 200, { status: 'cancelled', runId: 'run_2' })

    const user = userEvent.setup()
    const { notify } = renderRuns()
    await user.click(await screen.findByRole('button', { name: /^cancel$/i }))

    await waitFor(() => expect(server.requests).toContain('POST /runs/run_2/cancel'))
    await waitFor(() => expect(notify).toHaveBeenCalledWith('info', expect.stringMatching(/Cancellation requested/), expect.anything()))
  })

  it('does not offer cancel for a finished run', async () => {
    serveContract()
    renderRuns()
    const detail = await screen.findByText('Run ID')
    expect(within(detail.closest('.card') as HTMLElement).queryByRole('button', { name: /^cancel$/i })).toBeNull()
  })

  it('shows interrupted work as recovery required with no advancing duration or cancel', async () => {
    const server = serveContract()
    const interrupted = { ...runningRun, recoveryState: 'required', recoveryCommand: 'labctl runs recover run_2 --force' }
    server.override('GET', '/runs', 200, page([interrupted]))
    server.override('GET', '/runs/run_2', 200, { ...interrupted, steps: [{ index: 1, name: 'install', status: 'running' }] })
    server.override('GET', '/runs/run_2/logs', 200, { ...noLogs('running'), ...interrupted, done: false })
    renderRuns()
    expect((await screen.findAllByText('Recovery required')).length).toBeGreaterThan(0)
    expect(await screen.findByText('labctl runs recover run_2 --force')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^cancel$/i })).toBeNull()
    expect(screen.queryByText('streaming…')).toBeNull()
    expect(screen.queryByText(/^\d+s…$/)).toBeNull()
    expect(within(screen.getByText('Run ID').closest('.card') as HTMLElement).queryByText('running')).toBeNull()
  })

  it('shows the operator release audit and warns that execution may continue', async () => {
    const server = serveContract()
    const released = { ...runningRun, status: 'cancelled', recoveryState: 'released', releasedBy: 'uid=123 (operator)', releasedAt: '2026-10-27T10:00:00Z', ownerId: 'owner_former' }
    server.override('GET', '/runs', 200, page([released]))
    server.override('GET', '/runs/run_2', 200, { ...released, steps: [] })
    server.override('GET', '/runs/run_2/logs', 200, noLogs('cancelled'))
    renderRuns()
    expect((await screen.findAllByText('Key released by operator')).length).toBeGreaterThan(0)
    expect(screen.getByText('uid=123 (operator)')).toBeInTheDocument()
    expect(screen.getByText('2026-10-27T10:00:00Z')).toBeInTheDocument()
    expect(screen.getByText('owner_former')).toBeInTheDocument()
    expect(screen.getByText(/operation may still be running/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^cancel$/i })).toBeNull()
  })

  it('says why a hidden run shows no output', async () => {
    const server = serveContract()
    const hidden: RunSummary = { ...runningRun, id: 'run_9', kind: 'incident.inject', target: '(hidden)', status: 'succeeded' }
    server.override('GET', '/runs', 200, page([hidden]))
    server.override('GET', '/runs/run_9', 200, { ...hidden, steps: [] })
    server.override('GET', '/runs/run_9/logs', 403, {
      type: 'hidden_run', title: 'Forbidden', status: 403,
      detail: 'this run injected a silent incident: its output is withheld so it cannot name the fault',
    })
    renderRuns()
    expect(await screen.findByText(/output is withheld/)).toBeInTheDocument()
  })

  it('shows a designed error state when the list fails to load', async () => {
    const server = serveContract()
    server.override('GET', '/runs', 500, { type: 'internal_error', title: 'Internal Server Error', status: 500, detail: 'boom' })
    renderRuns()
    expect(await screen.findByText(/Failed to load runs/)).toBeInTheDocument()
  })
})
