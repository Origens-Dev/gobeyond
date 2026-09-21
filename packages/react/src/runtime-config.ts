/** Deployment configuration is public and exists before any application module runs. */
export interface RuntimeConfiguration {
  deploymentRevision?: string;
  publicConfig?: Readonly<Record<string, string>>;
}

const configurationElements = new WeakMap<Document, string>();

export function selectRuntimeConfigurationElement(target: Document, id: string): void {
  configurationElements.set(target, id);
}

export function readRuntimeConfiguration(target?: Document): RuntimeConfiguration {
  const doc = target ?? (typeof document === "undefined" ? undefined : document);
  const selected = doc && configurationElements.get(doc);
  const element = selected ? doc?.getElementById(selected) : (doc?.querySelector("script[data-gobeyond-bootstrap]") ?? doc?.getElementById("__GOBEYOND_DATA__"));
  const raw = element?.textContent;
  if (!raw) return {};
  const data: unknown = JSON.parse(raw);
  if (!data || typeof data !== "object") throw new Error("Invalid GoBeyond bootstrap configuration");
  const { publicConfig, deploymentRevision } = data as RuntimeConfiguration;
  if (deploymentRevision !== undefined && typeof deploymentRevision !== "string") throw new Error("Invalid deployment revision");
  if (publicConfig !== undefined && (!publicConfig || typeof publicConfig !== "object" || Array.isArray(publicConfig) || Object.values(publicConfig).some((value) => typeof value !== "string"))) throw new Error("Invalid public runtime configuration");
  return { deploymentRevision, publicConfig };
}

export function publicEnv(name: string, target?: Document): string | undefined {
  const values = readRuntimeConfiguration(target).publicConfig;
  return values && Object.prototype.hasOwnProperty.call(values, name) ? values[name] : undefined;
}
