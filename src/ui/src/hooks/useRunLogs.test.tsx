// SPDX-License-Identifier: Apache-2.0
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import type { RunLogs } from '../types'
import { useRunLogs } from './useRunLogs'

vi.mock('../api/client', async importOriginal => ({
  ...await importOriginal<typeof import('../api/client')>(),
  api: { getRunLogs: vi.fn() },
}))
import { api, ApiError } from '../api/client'
const getRunLogs = api.getRunLogs as unknown as Mock

const page = (texts: string[], firstSeq: number, done: boolean): RunLogs => ({
  runId: 'run-1',
  status: done ? 'succeeded' : 'running',
  lines: texts.map((text, i) => ({ seq: firstSeq + i, at: '2026-05-01T10:00:00Z', stream: 'stdout', text })),
  nextAfter: firstSeq + texts.length - 1,
  done,
})

describe('useRunLogs', () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    getRunLogs.mockReset()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('reads nothing when no run is selected', () => {
    const { result } = renderHook(() => useRunLogs(null))
    expect(result.current).toEqual({ lines: [], done: false, error: null })
    expect(getRunLogs).not.toHaveBeenCalled()
  })

  it('follows the cursor until the run is done, never repeating a line', async () => {
    getRunLogs
      .mockResolvedValueOnce(page(['one', 'two'], 1, false))
      .mockResolvedValueOnce(page([], 3, false))
      .mockResolvedValueOnce(page(['three'], 3, true))
    const { result } = renderHook(() => useRunLogs('run-1'))

    await waitFor(() => expect(result.current.lines.map(l => l.text)).toEqual(['one', 'two']))
    expect(getRunLogs).toHaveBeenLastCalledWith('run-1', 0)

    await act(() => vi.advanceTimersByTimeAsync(1000))
    expect(getRunLogs).toHaveBeenLastCalledWith('run-1', 2)
    await act(() => vi.advanceTimersByTimeAsync(1000))

    await waitFor(() => expect(result.current.done).toBe(true))
    expect(result.current.lines.map(l => l.text)).toEqual(['one', 'two', 'three'])

    // A finished run is not polled again.
    const calls = getRunLogs.mock.calls.length
    await act(() => vi.advanceTimersByTimeAsync(5000))
    expect(getRunLogs).toHaveBeenCalledTimes(calls)
  })

  it('shows the reason and stops when the server refuses the logs', async () => {
    getRunLogs.mockRejectedValueOnce(new ApiError('run not found', 404))
    const { result } = renderHook(() => useRunLogs('run-1'))

    await waitFor(() => expect(result.current.error).toBe('run not found'))
    await act(() => vi.advanceTimersByTimeAsync(10000))
    expect(getRunLogs).toHaveBeenCalledTimes(1)
  })

  it('drains every saved recovery page before stopping with recovery guidance', async () => {
    const recovery = { recoveryState: 'required', recoveryCommand: 'labctl runs recover run-1 --force' }
    getRunLogs
      .mockResolvedValueOnce({ ...page(['one'], 1, false), ...recovery })
      .mockResolvedValueOnce({ ...page(['two'], 2, false), ...recovery })
      .mockResolvedValueOnce({ ...page([], 3, false), ...recovery })
    const { result } = renderHook(() => useRunLogs('run-1'))
    await waitFor(() => expect(result.current.lines.map(l => l.text)).toEqual(['one']))
    expect(result.current.error).toBeNull()
    await act(() => vi.advanceTimersByTimeAsync(1000))
    expect(result.current.lines.map(l => l.text)).toEqual(['one', 'two'])
    await act(() => vi.advanceTimersByTimeAsync(1000))
    expect(result.current.done).toBe(false)
    expect(result.current.error).toContain('Recovery required')
    expect(result.current.error).toContain('labctl runs recover run-1 --force')
    await act(() => vi.advanceTimersByTimeAsync(10000))
    expect(getRunLogs).toHaveBeenCalledTimes(3)
  })

  it.each([
    ['the server cannot be reached', new Error('Cannot reach the labctl server')],
    ['the server fails', new ApiError('run store unavailable', 503)],
  ])('resumes from its cursor when %s for a moment', async (_, failure) => {
    getRunLogs
      .mockResolvedValueOnce(page(['one'], 1, false))
      .mockRejectedValueOnce(failure)
      .mockResolvedValueOnce(page(['two'], 2, true))
    const { result } = renderHook(() => useRunLogs('run-1'))
    await waitFor(() => expect(result.current.lines.map(l => l.text)).toEqual(['one']))

    await act(() => vi.advanceTimersByTimeAsync(1000))
    await waitFor(() => expect(result.current.error).toBe(failure.message))

    await act(() => vi.advanceTimersByTimeAsync(5000))
    await waitFor(() => expect(result.current.done).toBe(true))
    expect(getRunLogs).toHaveBeenLastCalledWith('run-1', 1)
    expect(result.current.lines.map(l => l.text)).toEqual(['one', 'two'])
    expect(result.current.error).toBeNull()
  })

  it('starts over when another run is selected', async () => {
    getRunLogs.mockImplementation((id: string) => Promise.resolve(page([`from ${id}`], 1, true)))
    const { result, rerender } = renderHook(({ id }) => useRunLogs(id), { initialProps: { id: 'run-1' } })
    await waitFor(() => expect(result.current.lines.map(l => l.text)).toEqual(['from run-1']))

    rerender({ id: 'run-2' })
    await waitFor(() => expect(result.current.lines.map(l => l.text)).toEqual(['from run-2']))
  })

  it('stops polling when the view is left', async () => {
    getRunLogs.mockResolvedValue(page([], 1, false))
    const { unmount } = renderHook(() => useRunLogs('run-1'))
    await waitFor(() => expect(getRunLogs).toHaveBeenCalledTimes(1))

    unmount()
    await act(() => vi.advanceTimersByTimeAsync(5000))
    expect(getRunLogs).toHaveBeenCalledTimes(1)
  })
})
