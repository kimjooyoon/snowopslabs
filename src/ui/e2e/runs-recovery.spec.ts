// SPDX-License-Identifier: Apache-2.0
import { expect, stubApi, test } from './fixtures'
import type { RunSummary } from '../src/types'

for (const recoveryState of ['required', 'released'] as const) {
  test(`shows ${recoveryState} work with recovery guidance and saved output`, async ({ page }) => {
    const run: RunSummary = {
      id: 'run-recovery', kind: 'platform.install', target: 'ingress',
      status: recoveryState === 'required' ? 'running' : 'cancelled',
      queuedAt: '2026-10-27T09:00:00Z', startedAt: '2026-10-27T09:00:00Z',
      ownerId: 'owner_former', recoveryState,
      ...(recoveryState === 'required'
        ? { recoveryCommand: 'labctl runs recover run-recovery --force' }
        : { releasedBy: 'uid=123 (operator)', releasedAt: '2026-10-27T10:00:00Z' }),
    }
    const reads: number[] = []
    await stubApi(page, {
      '/runs': { items: [run] },
      '/runs/run-recovery': { ...run, steps: [{ index: 1, name: 'install', status: 'running' }] },
      '/runs/run-recovery/logs': (url: URL) => {
        const after = Number(url.searchParams.get('after') ?? 0)
        reads.push(after)
        return {
          runId: run.id, status: run.status, recoveryState,
          recoveryCommand: run.recoveryCommand,
          lines: after === 0 ? [{ seq: 1, at: run.queuedAt, stream: 'stdout', text: 'saved partial output' }] : [],
          nextAfter: 1, done: recoveryState === 'released' && after === 1,
        }
      },
    })
    await page.goto('/runs')
    await expect(page.getByText('saved partial output')).toBeVisible()
    await expect(page.getByRole('button', { name: /^cancel$/i })).toHaveCount(0)
    await expect(page.getByText('streaming…')).toHaveCount(0)
    await expect(page.getByText('Last saved: running')).toBeVisible()
    if (recoveryState === 'required') {
      await expect(page.getByText('Recovery required', { exact: true }).first()).toBeVisible()
      await expect(page.getByText('labctl runs recover run-recovery --force', { exact: true })).toBeVisible()
      await expect(page.getByText(/Recovery required. Check the lab/)).toBeVisible()
    } else {
      await expect(page.getByText('Key released by operator', { exact: true }).first()).toBeVisible()
      await expect(page.getByText('uid=123 (operator)', { exact: true })).toBeVisible()
      await expect(page.getByText('2026-10-27T10:00:00Z', { exact: true })).toBeVisible()
      await expect(page.getByText(/operation may still be running/)).toBeVisible()
    }
    await expect.poll(() => reads).toEqual([0, 1])
  })
}
