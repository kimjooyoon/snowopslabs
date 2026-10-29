// SPDX-License-Identifier: Apache-2.0
//
// Scenarios view: activation shows a requirements preview (what it installs +
// what it needs) before anything runs, so nothing is a surprise. Requests go
// through the real client to the contract server (test/contract.ts).
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, it, expect, vi, beforeEach } from 'vitest'
import { page, serveContract, type ContractServer } from '../test/contract'
import { Scenarios } from './Scenarios'
import type { Scenario } from '../types'
import type { ConfirmRequest } from '../components/ConfirmDialog'

// Each test's contract server: goldens for anything it does not set.
let server: ContractServer
beforeEach(() => {
  server = serveContract()
})
afterEach(() => {
  vi.unstubAllGlobals()
})

/** Serves list as the scenario catalog, and each scenario's detail. */
function serveScenarios(list: Scenario[]) {
  server.override('GET', '/scenarios', 200, page(list))
  for (const sc of list) server.override('GET', `/scenarios/${sc.name}`, 200, sc)
}

/** The body of the activation the view sent for name, or undefined if none. */
function upCall(name: string) {
  return server.calls.find(c => c.method === 'POST' && c.path === `/scenarios/${name}/up`)
}

const gitops: Scenario = {
  name: 'gitops-cicd', displayName: 'GitOps & CI/CD', description: 'ArgoCD GitOps',
  category: 'delivery', active: false,
  prerequisites: { platform: ['ingress'], apps: [] },
  components: [
    { name: 'argocd', type: 'helm', namespace: 'argocd' },
    { name: 'argocd-apps', type: 'manifest', namespace: 'argocd' },
  ],
}

function renderScenarios() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  // requestConfirm is owned by the app shell; capture the request so we can
  // assert the preview and fire the confirm ourselves.
  let captured: ConfirmRequest | null = null
  const requestConfirm = (r: ConfirmRequest) => { captured = r }
  const notify = vi.fn()
  render(
    <QueryClientProvider client={client}>
      <Scenarios notify={notify} requestConfirm={requestConfirm} />
    </QueryClientProvider>,
  )
  return { getConfirm: () => captured, notify }
}

describe('Scenarios view - activation preview', () => {
  beforeEach(() => {
    server.override('GET', '/apps', 200, [
      { name: 'go-api', buildStrategy: 'docker', deployStrategy: 'helm', deployed: true, capabilities: ['prometheus-metrics', 'otlp-tracing'] },
      { name: 'java-api', buildStrategy: 'docker', deployStrategy: 'helm', deployed: true, capabilities: ['prometheus-metrics'] },
    ])
  })

  it('previews what a scenario installs and requires, then activates on confirm', async () => {
    const user = userEvent.setup()
    serveScenarios([{ ...gitops }])

    const { getConfirm } = renderScenarios()
    await user.click(await screen.findByRole('button', { name: /^activate$/i }))

    // Activation fetches detail and raises a confirm with the requirements.
    await waitFor(() => expect(server.requests).toContain('GET /scenarios/gitops-cicd'))
    await waitFor(() => expect(getConfirm()).not.toBeNull())
    const req = getConfirm()!
    render(<div>{req.message as React.ReactNode}</div>)
    expect(screen.getByText(/This will install/i)).toBeInTheDocument()
    expect(screen.getByText('argocd')).toBeInTheDocument()
    expect(screen.getByText(/Requires/i)).toBeInTheDocument()
    expect(within(screen.getByText(/Requires/i).parentElement as HTMLElement).getByText('ingress')).toBeInTheDocument()

    // scenarioUp only fires once the user confirms.
    expect(upCall('gitops-cicd')).toBeUndefined()
    req.onConfirm()
    // A scenario with no parameters activates with an empty override map.
    await waitFor(() => expect(upCall('gitops-cicd')).toBeDefined())
    expect(upCall('gitops-cicd')?.body).toBeUndefined()
  })

  it('previews components declared under stages (v2 scenarios), not just flat components', async () => {
    const user = userEvent.setup()
    // env-promotion-shaped: no top-level components, everything under stages.
    const staged: Scenario = {
      name: 'env-promotion', displayName: 'Multi-Env Promotion', description: 'promote',
      category: 'delivery', active: false,
      prerequisites: { platform: ['ingress'], apps: ['go-api'] },
      stages: [
        { name: 'seed-baseline', components: [{ name: 'baseline-image', type: 'script' }] },
        { name: 'dev-env', components: [{ name: 'dev-environment', type: 'manifest', namespace: 'env-dev' }] },
      ],
    }
    serveScenarios([{ ...staged }])

    const { getConfirm } = renderScenarios()
    await user.click(await screen.findByRole('button', { name: /^activate$/i }))
    await waitFor(() => expect(getConfirm()).not.toBeNull())
    render(<div>{getConfirm()!.message as React.ReactNode}</div>)

    expect(screen.getByText(/This will install/i)).toBeInTheDocument()
    expect(screen.getByText('baseline-image')).toBeInTheDocument()
    expect(screen.getByText('dev-environment')).toBeInTheDocument()
  })
})

describe('Scenarios view - parameters', () => {
  beforeEach(() => {
    server.override('GET', '/apps', 200, [
      { name: 'go-api', buildStrategy: 'docker', deployStrategy: 'helm', deployed: true, capabilities: ['prometheus-metrics', 'otlp-tracing'] },
      { name: 'java-api', buildStrategy: 'docker', deployStrategy: 'helm', deployed: true, capabilities: ['prometheus-metrics'] },
    ])
  })

  const withParams: Scenario = {
    ...gitops,
    parameters: [
      { name: 'Threshold', displayName: 'RPS per replica', default: '25', type: 'int', min: 1, max: 500 },
    ],
  }

  it('shows tunable parameters seeded to defaults and submits overrides on confirm', async () => {
    const user = userEvent.setup()
    serveScenarios([{ ...withParams }])

    const { getConfirm } = renderScenarios()
    await user.click(await screen.findByRole('button', { name: /^activate$/i }))
    await waitFor(() => expect(getConfirm()).not.toBeNull())
    const req = getConfirm()!
    render(<div>{req.message as React.ReactNode}</div>)

    const input = screen.getByLabelText(/RPS per replica/i) as HTMLInputElement
    expect(input.value).toBe('25') // seeded to the declared default

    await user.clear(input)
    await user.type(input, '15')

    req.onConfirm()
    await waitFor(() => expect(upCall('gitops-cicd')?.body).toEqual({ params: { Threshold: '15' } }))
  })
})

describe('Scenarios view - detail modal teaches implementation', () => {
  beforeEach(() => {
    server.override('GET', '/apps', 200, [
      { name: 'go-api', buildStrategy: 'docker', deployStrategy: 'helm', deployed: true, capabilities: ['prometheus-metrics', 'otlp-tracing'] },
      { name: 'java-api', buildStrategy: 'docker', deployStrategy: 'helm', deployed: true, capabilities: ['prometheus-metrics'] },
    ])
  })

  const rich: Scenario = {
    ...gitops,
    objectives: ['Declare a KEDA ScaledObject'],
    snippets: [{ label: 'The ScaledObject', path: 'manifests/scaledobject.yaml', yaml: 'kind: ScaledObject\nthreshold: 25' }],
    checks: [{ name: 'keda-hpa-created', type: 'kubectl', resource: 'hpa/keda-hpa-go-api', operator: 'exists' }],
  }

  it('groups the sections into tabs and lands on Overview', async () => {
    const user = userEvent.setup()
    serveScenarios([{ ...rich }])

    renderScenarios()
    await user.click(await screen.findByRole('button', { name: /details/i }))

    // Overview is selected on open; the heavier sections are behind their tabs
    // so the modal opens on one readable screen.
    expect(await screen.findByRole('tab', { name: /overview/i })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByText(/What you'll learn/i)).toBeInTheDocument()
    expect(screen.getByText('Declare a KEDA ScaledObject')).toBeInTheDocument()
    expect(screen.queryByText(/How it's implemented/i)).not.toBeInTheDocument()
    expect(screen.queryByText('keda-hpa-created')).not.toBeInTheDocument()
  })

  it('shows the implementation snippet and the success checks on their tabs', async () => {
    const user = userEvent.setup()
    serveScenarios([{ ...rich }])

    renderScenarios()
    await user.click(await screen.findByRole('button', { name: /details/i }))

    await user.click(await screen.findByRole('tab', { name: /implementation/i }))
    expect(screen.getByText(/How it's implemented/i)).toBeInTheDocument()
    expect(screen.getByText(/kind: ScaledObject/)).toBeInTheDocument()

    await user.click(screen.getByRole('tab', { name: /checks/i }))
    expect(screen.getByText('keda-hpa-created')).toBeInTheDocument()
    expect(screen.getByText(/hpa\/keda-hpa-go-api/)).toBeInTheDocument()
  })

  // A scenario states what it needs of the bound app; the reader must be able to
  // see that requirement without opening scenario.yaml.
  it('shows required workload capabilities alongside the other prerequisites', async () => {
    const user = userEvent.setup()
    const needsCaps: Scenario = {
      ...rich,
      prerequisites: { platform: ['ingress'], apps: ['{{.WorkloadName}}'], capabilities: ['prometheus-metrics', 'otlp-tracing'] },
      workload: { app: 'go-api', namespace: 'go-api', service: 'go-api.go-api.svc.cluster.local', port: '8080', metric: 'http_server_request_duration_seconds' },
    }
    serveScenarios([{ ...needsCaps }])

    renderScenarios()
    await user.click(await screen.findByRole('button', { name: /details/i }))

    expect(await screen.findByText(/Platform: ingress/)).toBeInTheDocument()
    expect(screen.getByText(/Needs: prometheus-metrics/)).toBeInTheDocument()
    expect(screen.getByText(/Needs: otlp-tracing/)).toBeInTheDocument()
  })

  // The app a scenario resolved from the workload binding is not a prerequisite.
  // Showing it as "requires go-api" told users that content which runs against
  // any conforming app only ran against one.
  it('shows the bound workload as what it runs against, not as a requirement', async () => {
    const user = userEvent.setup()
    const bound: Scenario = {
      ...rich,
      prerequisites: { platform: ['ingress'], apps: ['{{.WorkloadName}}'] },
      pinnedApps: [],
      workload: { app: 'java-api', namespace: 'java-api', service: 'java-api.java-api.svc.cluster.local', port: '8080', metric: 'http_server_request_duration_seconds' },
    }
    serveScenarios([{ ...bound }])

    renderScenarios()
    await user.click(await screen.findByRole('button', { name: /details/i }))

    expect(await screen.findByRole('heading', { name: /runs against/i })).toBeInTheDocument()
    expect(screen.getAllByText('java-api').length).toBeGreaterThan(0)
    expect(screen.queryByText(/App: java-api/)).not.toBeInTheDocument()
    expect(screen.queryByText(/\{\{\.WorkloadName\}\}/)).not.toBeInTheDocument()
  })

  // Preflight refuses an app that is missing a required capability, so offering
  // it would trade a clear "this app cannot run this" for a failed run.
  it('will not let an app be chosen that cannot satisfy the scenario', async () => {
    const user = userEvent.setup()
    const needsTracing: Scenario = {
      ...gitops,
      prerequisites: { platform: [], apps: [], capabilities: ['prometheus-metrics', 'otlp-tracing'] },
      workload: { app: 'go-api', namespace: 'go-api', service: 'go-api.go-api.svc.cluster.local', port: '8080', metric: 'm' },
    }
    serveScenarios([{ ...needsTracing }])

    const { getConfirm } = renderScenarios()
    await user.click(await screen.findByRole('button', { name: /^activate$/i }))
    await waitFor(() => expect(getConfirm()).not.toBeNull())
    render(<div>{getConfirm()!.message as React.ReactNode}</div>)

    await screen.findByLabelText(/application to run against/i)
    // java-api declares prometheus-metrics but not otlp-tracing.
    const blocked = screen.getByRole('option', { name: /java-api/ }) as HTMLOptionElement
    expect(blocked.disabled).toBe(true)
    expect(blocked.text).toMatch(/no otlp-tracing/)
    expect((screen.getByRole('option', { name: 'go-api' }) as HTMLOptionElement).disabled).toBe(false)
  })

  // The workload choice is the other half of "activate this scenario", and it
  // was reachable only from the CLI's --app flag.
  it('offers the workload as a choice at activation and submits it', async () => {
    const user = userEvent.setup()
    const bound: Scenario = {
      ...gitops,
      workload: { app: 'go-api', namespace: 'go-api', service: 'go-api.go-api.svc.cluster.local', port: '8080', metric: 'http_server_request_duration_seconds' },
    }
    serveScenarios([{ ...bound }])

    const { getConfirm } = renderScenarios()
    await user.click(await screen.findByRole('button', { name: /^activate$/i }))
    await waitFor(() => expect(getConfirm()).not.toBeNull())
    const req = getConfirm()!
    render(<div>{req.message as React.ReactNode}</div>)

    const picker = await screen.findByLabelText(/application to run against/i)
    await user.selectOptions(picker, 'java-api')

    req.onConfirm()
    await waitFor(() => expect(upCall('gitops-cicd')?.body).toEqual({ app: 'java-api' }))
  })

  it('moves between tabs with the arrow keys', async () => {
    const user = userEvent.setup()
    serveScenarios([{ ...rich }])

    renderScenarios()
    await user.click(await screen.findByRole('button', { name: /details/i }))

    const overview = await screen.findByRole('tab', { name: /overview/i })
    overview.focus()
    await user.keyboard('{ArrowRight}')
    expect(screen.getByRole('tab', { name: /implementation/i })).toHaveAttribute('aria-selected', 'true')
  })
})

describe('Scenarios view - verify', () => {
  beforeEach(() => {
    server.override('GET', '/apps', 200, [
      { name: 'go-api', buildStrategy: 'docker', deployStrategy: 'helm', deployed: true, capabilities: ['prometheus-metrics', 'otlp-tracing'] },
      { name: 'java-api', buildStrategy: 'docker', deployStrategy: 'helm', deployed: true, capabilities: ['prometheus-metrics'] },
    ])
  })

  const active: Scenario = { ...gitops, active: true }

  it('is disabled while the scenario is inactive', async () => {
    serveScenarios([{ ...gitops }]) // inactive
    renderScenarios()
    const verifyBtn = await screen.findByRole('button', { name: /verify/i })
    expect(verifyBtn).toBeDisabled()
  })

  it('runs the checks and renders the per-check pass/fail breakdown', async () => {
    const user = userEvent.setup()
    serveScenarios([active])
    server.override('POST', '/scenarios/gitops-cicd/verify', 200, {
      scenario: 'gitops-cicd',
      passed: false,
      results: [
        { name: 'argocd-ready', type: 'kubectl', pass: true, got: '1', want: '>= 1' },
        { name: 'app-synced', type: 'kubectl', pass: false, got: '0', want: '>= 1' },
      ],
    })

    renderScenarios()
    await user.click(await screen.findByRole('button', { name: /verify/i }))

    await waitFor(() => expect(server.requests).toContain('POST /scenarios/gitops-cicd/verify'))
    // Both checks render with their names and PASS/FAIL states.
    expect(await screen.findByText('argocd-ready')).toBeInTheDocument()
    expect(screen.getByText('app-synced')).toBeInTheDocument()
    expect(screen.getByText('PASS')).toBeInTheDocument()
    expect(screen.getByText('FAIL')).toBeInTheDocument()
  })

  it('names the failing checks in the notification, not just how many', async () => {
    const user = userEvent.setup()
    serveScenarios([active])
    server.override('POST', '/scenarios/gitops-cicd/verify', 200, {
      scenario: 'gitops-cicd',
      passed: false,
      results: [
        { name: 'argocd-ready', type: 'kubectl', pass: true, got: '1', want: '>= 1' },
        { name: 'app-synced', type: 'kubectl', pass: false, got: '0', want: '>= 1' },
        { name: 'drift-healed', type: 'script', pass: false, got: '', want: '' },
      ],
    })

    const { notify } = renderScenarios()
    await user.click(await screen.findByRole('button', { name: /verify/i }))
    await waitFor(() => expect(notify).toHaveBeenCalledWith(
      'info', '2 of 3 checks failing', expect.stringMatching(/^Failing: app-synced, drift-healed\./),
    ))
  })

  it('says the scenario is in its expected state when every check passes', async () => {
    const user = userEvent.setup()
    serveScenarios([active])
    server.override('POST', '/scenarios/gitops-cicd/verify', 200, {
      scenario: 'gitops-cicd',
      passed: true,
      results: [
        { name: 'argocd-ready', type: 'kubectl', pass: true, got: '1', want: '>= 1' },
        { name: 'app-synced', type: 'kubectl', pass: true, got: '1', want: '>= 1' },
      ],
    })

    renderScenarios()
    await user.click(await screen.findByRole('button', { name: /verify/i }))
    expect(await screen.findByText('All 2 checks passed - the scenario is in its expected state.')).toBeInTheDocument()
  })
})
