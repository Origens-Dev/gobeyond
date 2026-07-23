export const RENDER_PLAN_API_VERSION = 'gobeyond.render/v1alpha1' as const

export type RenderPlan = {
  apiVersion: typeof RENDER_PLAN_API_VERSION
  routeId: string
  root: PlanNode
}

export type PlanNode =
  | ElementNode
  | TextNode
  | FragmentNode
  | ConditionalNode
  | EachNode
  | ClientOnlyNode
  | RawHTMLNode

export type ElementNode = {
  kind: 'element'
  tag: string
  namespace?: 'html' | 'svg'
  attributes?: Attribute[]
  children?: PlanNode[]
}

export type Attribute = {
  name: string
  value: PlanExpression
  mode?: 'string' | 'boolean' | 'url' | 'style'
}

export type TextNode = { kind: 'text'; value: PlanExpression }
export type FragmentNode = { kind: 'fragment'; children: PlanNode[] }
export type ConditionalNode = {
  kind: 'conditional'
  test: PlanExpression
  consequent: PlanNode
  alternate?: PlanNode
}
export type EachNode = {
  kind: 'each'
  items: PlanExpression
  item: string
  index?: string
  key: PlanExpression
  body: PlanNode
}
export type ClientOnlyNode = { kind: 'clientOnly'; fallback?: PlanNode | null }
export type RawHTMLNode = { kind: 'rawHtml'; value: PlanExpression }

export type PlanExpression =
  | { kind: 'literal'; value: unknown }
  | { kind: 'path'; path: Array<string | number> }
  | {
      kind: 'binary'
      operator:
        | '+'
        | '-'
        | '*'
        | '/'
        | '%'
        | '=='
        | '!='
        | '<'
        | '<='
        | '>'
        | '>='
        | '&&'
        | '||'
        | '??'
      left: PlanExpression
      right: PlanExpression
    }
  | { kind: 'unary'; operator: '!' | '-'; operand: PlanExpression }
  | {
      kind: 'helper'
      name: 'string' | 'lower' | 'upper' | 'join' | 'url' | 'style'
      arguments: PlanExpression[]
    }
  | {
      kind: 'intrinsic'
      name: string
      arguments: PlanExpression[]
    }

export type Diagnostic = {
  code: string
  message: string
  suggestion?: string
  fileName: string
  start: number
  length: number
  line: number
  column: number
}

export type CompileOptions = {
  sourceText: string
  fileName?: string
  routeId: string
  componentName?: string
}

export type CompileResult =
  | {
      ok: true
      plan: RenderPlan
      clientBoundaries: ClientBoundaryRecord[]
      diagnostics: []
    }
  | { ok: false; diagnostics: Diagnostic[] }

export const CLIENT_BOUNDARY_API_VERSION =
  'gobeyond.client-boundaries/v1alpha1' as const

/**
 * One compiler-approved downgrade at either a JSX call site or a route's root
 * component. `source`, `boundary`, `start`, and `end` are stable Vite inputs;
 * diagnostics and other compiler failures never appear in this list.
 */
export type ClientBoundaryRecord = {
  id: string
  routeId: string
  /** Project-relative module containing the transformed call site/component. */
  source: string
  component: string
  /** Project-relative module containing the nearest "use client" directive. */
  boundary: string
  reason: string
  target: 'callSite' | 'component'
  start: number
  end: number
  line: number
  column: number
}

export type ClientBoundaryManifest = {
  apiVersion: typeof CLIENT_BOUNDARY_API_VERSION
  boundaries: ClientBoundaryRecord[]
}

export type SourceRoot = {
  /** Import prefix such as "@/" or "#components/". */
  prefix: string
  /** Absolute path, or a path relative to projectRoot. */
  directory: string
}

export type CompileFileOptions = {
  entryFile: string
  routeId: string
  componentName?: string
  projectRoot?: string
  sourceRoots?: SourceRoot[]
  /** Optional app root used to discover applicable layout files. */
  appDirectory?: string
}

export type ProjectRoute = {
  routeId: string
  entryFile: string
  componentName?: string
  /** Defaults to page.schema.ts beside entryFile. */
  schemaFile?: string
  /** Defaults to actions.ts beside entryFile when that file exists. */
  actionsFile?: string
  /**
   * Static routes are evaluated at build time. Omit this for dynamic routes or
   * when only plans/contracts should be compiled.
   */
  kind?: 'static' | 'dynamic'
  /** Defaults to page.build.ts beside entryFile. Missing files are allowed. */
  buildFile?: string
  /** Defaults to page.metadata.ts beside entryFile. Missing files are allowed. */
  metadataFile?: string
  /**
   * Public route pattern used to validate generated params. When omitted, the
   * compiler derives params from bracketed entry-file directory segments.
   */
  routePattern?: string
}

export type CompileProjectOptions = {
  projectRoot: string
  routes: ProjectRoute[]
  sourceRoots?: SourceRoot[]
  /** Optional app root used to discover applicable layout files. */
  appDirectory?: string
}

export const COMPILER_PROJECT_API_VERSION = 'gobeyond.compiler-project/v1alpha1' as const

export type ProjectPlans = {
  apiVersion: typeof COMPILER_PROJECT_API_VERSION
  plans: RenderPlan[]
  contracts: ValueContracts
  /** Deterministic source-module composition for the browser route registry. */
  routeModules: ProjectRouteModules[]
  /** Exact compiler-approved client-only transforms for the browser build. */
  clientBoundaries: ClientBoundaryManifest
  staticBuild: StaticBuildArtifact
}

export type ProjectRouteModules = {
  routeId: string
  entryFile: string
  /** Project-relative React layouts, ordered outermost to innermost. */
  layoutFiles: string[]
}

export type CompileProjectResult =
  | { ok: true; output: ProjectPlans; diagnostics: [] }
  | { ok: false; diagnostics: Diagnostic[] }

export const VALUE_CONTRACT_API_VERSION = 'gobeyond.contract/v1alpha1' as const

export type ValueSchema = {
  kind:
    | 'string'
    | 'number'
    | 'integer'
    | 'boolean'
    | 'datetime'
    | 'bytes'
    | 'safeHtml'
    | 'literal'
    | 'enum'
    | 'array'
    | 'object'
    | 'union'
  optional?: boolean
  nullable?: boolean
  value?: string | number | boolean | null
  values?: string[]
  items?: ValueSchema
  shape?: Record<string, ValueSchema>
  variants?: ValueSchema[]
}

export type RouteValueContract = {
  routeId: string
  props: ValueSchema
}

export type ActionValueContract = {
  actionId: string
  input: ValueSchema
  output: ValueSchema
}

export type ValueContracts = {
  apiVersion: typeof VALUE_CONTRACT_API_VERSION
  routes: RouteValueContract[]
  actions: ActionValueContract[]
}

export const STATIC_BUILD_API_VERSION = 'gobeyond.static-build/v1alpha1' as const

export type StaticParam = string | string[]

export type StaticBuildEntry = {
  params: Record<string, StaticParam>
  props: unknown
  /** Serializable document metadata resolved from page.metadata.ts. */
  metadata?: Record<string, unknown>
}

export type StaticRouteArtifact = {
  routeId: string
  /** Project-relative build-only module, when one exists. */
  buildFile?: string
  /** Project-relative build-only metadata module, when one exists. */
  metadataFile?: string
  /** Project-relative React layouts, ordered outermost to innermost. */
  layoutFiles: string[]
  entries: StaticBuildEntry[]
}

export type StaticBuildArtifact = {
  apiVersion: typeof STATIC_BUILD_API_VERSION
  routes: StaticRouteArtifact[]
}

export type CompileStaticRouteOptions = {
  projectRoot: string
  route: ProjectRoute
  contract: RouteValueContract
  /** Optional app root used for deterministic layout discovery. */
  appDirectory?: string
  /** Maximum build-module execution time. Defaults to 30 seconds. */
  timeoutMs?: number
}

export type CompileStaticRouteResult =
  | { ok: true; artifact: StaticRouteArtifact; diagnostics: [] }
  | { ok: false; diagnostics: Diagnostic[] }

export type ContractSourceOptions = {
  sourceText: string
  fileName?: string
  routeId: string
}

export type PageContractCompileResult =
  | { ok: true; contract: RouteValueContract; diagnostics: [] }
  | { ok: false; diagnostics: Diagnostic[] }

export type ActionContractCompileResult =
  | { ok: true; contracts: ActionValueContract[]; diagnostics: [] }
  | { ok: false; diagnostics: Diagnostic[] }
