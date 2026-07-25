import type {
  ClientBoundaryRecord,
  PlanNode,
  PortabilityDowngrade,
  PortabilityReport,
  PORTABILITY_REPORT_API_VERSION,
  RenderPlan,
} from './types.js'

export const PORTABILITY_REPORT_VERSION =
  'gobeyond.portability-report/v1alpha1' as const satisfies typeof PORTABILITY_REPORT_API_VERSION

/**
 * Build a per-route portability report from compiled plans and client-boundary
 * records so adopters can rank fixes (`gobeyond report portability`).
 */
export function buildPortabilityReport(
  plans: readonly RenderPlan[],
  boundaries: readonly ClientBoundaryRecord[],
): PortabilityReport {
  const byRoute = new Map<string, ClientBoundaryRecord[]>()
  for (const boundary of boundaries) {
    const list = byRoute.get(boundary.routeId) ?? []
    list.push(boundary)
    byRoute.set(boundary.routeId, list)
  }
  const planByRoute = new Map(plans.map((plan) => [plan.routeId, plan]))
  const routes: PortabilityReport['routes'] = []
  const routeIds = new Set([...byRoute.keys(), ...planByRoute.keys()])
  for (const routeId of [...routeIds].sort()) {
    const routeBoundaries = byRoute.get(routeId) ?? []
    const plan = planByRoute.get(routeId)
    const totalNodes = plan ? countPortableNodes(plan.root) : 0
    const clientOnlyNodes = plan ? countClientOnlyNodes(plan.root) : 0
    const routeShare =
      totalNodes > 0 ? Math.min(1, clientOnlyNodes / totalNodes) : undefined
    const downgrades: PortabilityDowngrade[] = routeBoundaries.map(
      (boundary) => {
        const share =
          routeShare !== undefined && routeBoundaries.length > 0
            ? routeShare / routeBoundaries.length
            : undefined
        return {
          routeId: boundary.routeId,
          component: boundary.component,
          source: boundary.source,
          boundary: boundary.boundary,
          reason: boundary.reason,
          ...(boundary.triggerCode === undefined
            ? {}
            : { triggerCode: boundary.triggerCode }),
          ...(boundary.triggerConstruct === undefined
            ? {}
            : { triggerConstruct: boundary.triggerConstruct }),
          ...(boundary.suggestion === undefined
            ? {}
            : { suggestion: boundary.suggestion }),
          ...(share === undefined ? {} : { markupLostShare: share }),
        }
      },
    )
    routes.push({
      routeId,
      downgrades,
      totalMarkupLostShare: routeShare ?? 0,
    })
  }
  return {
    apiVersion: PORTABILITY_REPORT_VERSION,
    routes,
  }
}

function countPortableNodes(node: PlanNode): number {
  switch (node.kind) {
    case 'element':
      return (
        1 +
        (node.children ?? []).reduce(
          (sum, child) => sum + countPortableNodes(child),
          0,
        )
      )
    case 'fragment':
      return node.children.reduce(
        (sum, child) => sum + countPortableNodes(child),
        0,
      )
    case 'conditional':
      return (
        1 +
        countPortableNodes(node.consequent) +
        (node.alternate ? countPortableNodes(node.alternate) : 0)
      )
    case 'each':
      return 1 + countPortableNodes(node.body)
    case 'clientOnly':
      return (
        1 + (node.fallback ? countPortableNodes(node.fallback) : 0)
      )
    case 'text':
    case 'rawHtml':
      return 1
    default:
      return 0
  }
}

function countClientOnlyNodes(node: PlanNode): number {
  switch (node.kind) {
    case 'clientOnly':
      return 1
    case 'element':
      return (node.children ?? []).reduce(
        (sum, child) => sum + countClientOnlyNodes(child),
        0,
      )
    case 'fragment':
      return node.children.reduce(
        (sum, child) => sum + countClientOnlyNodes(child),
        0,
      )
    case 'conditional':
      return (
        countClientOnlyNodes(node.consequent) +
        (node.alternate ? countClientOnlyNodes(node.alternate) : 0)
      )
    case 'each':
      return countClientOnlyNodes(node.body)
    default:
      return 0
  }
}

/** Format a portability report for CLI stdout. */
export function formatPortabilityReport(report: PortabilityReport): string {
  if (report.routes.every((route) => route.downgrades.length === 0)) {
    return 'No client-boundary downgrades recorded.\n'
  }
  const lines: string[] = ['Portability report', '']
  for (const route of report.routes) {
    if (route.downgrades.length === 0) continue
    const lost = Math.round(route.totalMarkupLostShare * 100)
    lines.push(
      `Route ${route.routeId} — ~${lost}% markup behind client boundaries`,
    )
    for (const downgrade of route.downgrades) {
      const trigger =
        downgrade.triggerCode && downgrade.triggerConstruct
          ? `${downgrade.triggerCode} (${downgrade.triggerConstruct})`
          : downgrade.triggerCode ?? downgrade.triggerConstruct ?? 'unknown'
      lines.push(`  • ${downgrade.component} @ ${downgrade.source}`)
      lines.push(`      trigger: ${trigger}`)
      if (downgrade.suggestion) {
        lines.push(`      hint: ${downgrade.suggestion}`)
      }
    }
    lines.push('')
  }
  return `${lines.join('\n').trimEnd()}\n`
}
