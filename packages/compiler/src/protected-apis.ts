/**
 * Protected React API registry.
 *
 * Import-tracked APIs only (`from 'react'` / `react/index`). The compiler bakes
 * or records rewrite sites; Vite consumes matching site kinds. Keep
 * `docs/architecture.md`, `packages/compiler/README.md`, and the root README
 * in sync when shipping a new entry.
 *
 * Test matrix convention for every new API:
 * - compiler success case
 * - compiler failure / diagnostic case
 * - Vite or hydration case when strategy is `rewrite`
 */

export type ProtectedApiStrategy =
  | 'bake'
  | 'rewrite'
  | 'passthrough'
  | 'reject'

export type ProtectedApiEntry = {
  /** Canonical export name from `react`. */
  name: string
  /** Modules that may introduce the binding. */
  modules: readonly string[]
  /** Strategy applied during portable compile. */
  strategy: ProtectedApiStrategy
  /** Diagnostic codes commonly emitted for this API. */
  diagnostics: readonly string[]
  /** Arity when the API is a call (undefined for components/types). */
  arity?: number | { min: number; max: number }
  /** Vite rewrite site kind when strategy includes browser rewrite. */
  viteSiteKind?: 'useId' | 'dateIntrinsic'
  notes: string
}

export const PROTECTED_REACT_MODULES = [
  'react',
  'react/index.js',
  'react/index',
] as const

export const PROTECTED_APIS: readonly ProtectedApiEntry[] = [
  {
    name: 'useId',
    modules: PROTECTED_REACT_MODULES,
    strategy: 'rewrite',
    diagnostics: ['GB1084', 'GB1085', 'GB1088'],
    arity: 0,
    viteSiteKind: 'useId',
    notes:
      'Span-stable call-site ids; parametric under keyed .map; nested map inlines bake parametric plan values.',
  },
  {
    name: 'useMemo',
    modules: PROTECTED_REACT_MODULES,
    strategy: 'bake',
    diagnostics: ['GB1076', 'GB1085'],
    arity: { min: 1, max: 2 },
    notes: 'Inline () => portable-expr factory into the plan.',
  },
  {
    name: 'useCallback',
    modules: PROTECTED_REACT_MODULES,
    strategy: 'bake',
    diagnostics: ['GB1076', 'GB1085'],
    arity: { min: 1, max: 2 },
    notes:
      'Same portable-factory rules as useMemo; typical on* usages are stripped from markup.',
  },
  {
    name: 'useState',
    modules: PROTECTED_REACT_MODULES,
    strategy: 'bake',
    diagnostics: ['GB1022', 'GB1023', 'GB1085'],
    arity: { min: 0, max: 1 },
    notes: 'Initial state / lazy () => expr only; setters are browser-only.',
  },
  {
    name: 'useRef',
    modules: PROTECTED_REACT_MODULES,
    strategy: 'bake',
    diagnostics: ['GB1022', 'GB1023', 'GB1085'],
    arity: { min: 0, max: 1 },
    notes:
      'Bake initial .current like useState; mutation is ignored for markup; ref={} attributes are stripped.',
  },
  {
    name: 'useReducer',
    modules: PROTECTED_REACT_MODULES,
    strategy: 'bake',
    diagnostics: ['GB1022', 'GB1076', 'GB1085'],
    arity: { min: 2, max: 3 },
    notes: 'Initial state or lazy init (initArg, init) only.',
  },
  {
    name: 'useContext',
    modules: PROTECTED_REACT_MODULES,
    strategy: 'bake',
    diagnostics: ['GB1076', 'GB1085'],
    arity: 1,
    notes: 'Reads nearest same-module Provider value in the portable tree.',
  },
  {
    name: 'createContext',
    modules: PROTECTED_REACT_MODULES,
    strategy: 'bake',
    diagnostics: ['GB1076'],
    arity: { min: 0, max: 1 },
    notes: 'Marks a context local for Provider / useContext pairing.',
  },
  {
    name: 'Suspense',
    modules: PROTECTED_REACT_MODULES,
    strategy: 'passthrough',
    diagnostics: [],
    notes: 'Children only; no streaming; fallback never reaches initial markup.',
  },
  {
    name: 'Fragment',
    modules: PROTECTED_REACT_MODULES,
    strategy: 'bake',
    diagnostics: ['GB1064', 'GB1095'],
    notes: 'Keyed Fragment / React.Fragment allowed as .map roots; non-key props rejected.',
  },
  {
    name: 'Children',
    modules: PROTECTED_REACT_MODULES,
    strategy: 'bake',
    diagnostics: ['GB1096'],
    notes: 'Static Children.map / Children.toArray over JSX children only.',
  },
  {
    name: 'createElement',
    modules: PROTECTED_REACT_MODULES,
    strategy: 'bake',
    diagnostics: ['GB1091', 'GB1092', 'GB1050'],
    notes: 'Static string types and known props; components via identifier.',
  },
  {
    name: 'cloneElement',
    modules: PROTECTED_REACT_MODULES,
    strategy: 'bake',
    diagnostics: ['GB1097'],
    notes: 'Only when the element is a portable JSX expression and props merge statically.',
  },
  {
    name: 'lazy',
    modules: PROTECTED_REACT_MODULES,
    strategy: 'reject',
    diagnostics: ['GB1098'],
    notes: 'Must wrap in ClientOnly or live under use client; not baked.',
  },
]

/** Date projection getters rewritten to renderSnapshotDate() by Vite. */
export const DATE_PROJECTION_GETTERS = [
  'getFullYear',
  'getUTCFullYear',
  'getMonth',
  'getUTCMonth',
  'getDate',
  'getUTCDate',
  'getHours',
  'getUTCHours',
  'getMinutes',
  'getUTCMinutes',
  'getSeconds',
  'getUTCSeconds',
] as const

export type DateProjectionGetter = (typeof DATE_PROJECTION_GETTERS)[number]

export const DATE_INTRINSIC_API: ProtectedApiEntry = {
  name: 'Date',
  modules: [],
  strategy: 'rewrite',
  diagnostics: [],
  viteSiteKind: 'dateIntrinsic',
  notes:
    'Zero-arg new Date().get* / getUTC* bake to render-snapshot intrinsics; Vite rewrites to renderSnapshotDate(). Prefer UTC getters.',
}

export const PROTECTED_GOBEYOND_REACT_MODULES = [
  '@go-beyond/react',
  '@go-beyond/react/index.js',
  '@go-beyond/react/index',
] as const

/** First-party hooks baked from request / soft-navigation state. */
export const PROTECTED_GOBEYOND_APIS: readonly ProtectedApiEntry[] = [
  {
    name: 'usePathname',
    modules: PROTECTED_GOBEYOND_REACT_MODULES,
    strategy: 'bake',
    diagnostics: ['GB1085'],
    arity: 0,
    notes:
      'Request pathname for active-nav; browser reads soft-navigation route so hydration matches.',
  },
  {
    name: 'useRoute',
    modules: PROTECTED_GOBEYOND_REACT_MODULES,
    strategy: 'bake',
    diagnostics: ['GB1085'],
    arity: 0,
    notes:
      'Route id + params from the active request; browser reads soft-navigation route.',
  },
]

const hookNames = new Set(
  [
    ...PROTECTED_APIS.filter((entry) =>
      [
        'useId',
        'useMemo',
        'useCallback',
        'useState',
        'useRef',
        'useReducer',
        'useContext',
        'createContext',
        'Suspense',
        'Fragment',
        'Children',
        'lazy',
        'cloneElement',
        'createElement',
      ].includes(entry.name),
    ).map((entry) => entry.name),
    ...PROTECTED_GOBEYOND_APIS.map((entry) => entry.name),
  ],
)

export type ProtectedHookName =
  | 'useId'
  | 'useMemo'
  | 'useCallback'
  | 'useState'
  | 'useRef'
  | 'useReducer'
  | 'useContext'
  | 'createContext'
  | 'Suspense'
  | 'Fragment'
  | 'Children'
  | 'lazy'
  | 'cloneElement'
  | 'createElement'
  | 'usePathname'
  | 'useRoute'

export function isProtectedHookName(name: string): name is ProtectedHookName {
  return hookNames.has(name)
}

export function isProtectedReactModule(specifier: string): boolean {
  return (PROTECTED_REACT_MODULES as readonly string[]).includes(specifier)
}

export function isProtectedGoBeyondReactModule(specifier: string): boolean {
  return (PROTECTED_GOBEYOND_REACT_MODULES as readonly string[]).includes(
    specifier,
  )
}

export function isProtectedApiModule(specifier: string): boolean {
  return (
    isProtectedReactModule(specifier) ||
    isProtectedGoBeyondReactModule(specifier)
  )
}

export function dateIntrinsicName(getter: string): string | undefined {
  if (!(DATE_PROJECTION_GETTERS as readonly string[]).includes(getter)) {
    return undefined
  }
  return `ecmascript.Date.prototype.${getter}`
}
