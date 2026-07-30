/**
 * Portable durable client. Requires a configured backend (e.g. @origens-dev/temporal).
 * There is no implicit backend.
 */

export type WorkflowStartOptions = {
  workflowName: string;
  args?: unknown[];
  workflowId?: string;
  taskQueue?: string;
};

export type WorkflowSignalOptions = {
  workflowId: string;
  signalName: string;
  args?: unknown[];
};

export type WorkflowHandle = {
  workflowId: string;
  runId?: string;
};

/**
 * A backend executes trigger operations and wakes workers when needed.
 * Hosted backends call platform APIs; local backends talk to Docker Temporal.
 */
export type WorkflowBackend = {
  start(options: WorkflowStartOptions): Promise<WorkflowHandle>;
  signal(options: WorkflowSignalOptions): Promise<void>;
};

let configuredBackend: WorkflowBackend | undefined;

export function use(backend: WorkflowBackend): void {
  configuredBackend = backend;
}

function requireBackend(): WorkflowBackend {
  if (!configuredBackend) {
    throw new Error(
      "@go-beyond/workflows: no backend configured. Call workflows.use(createTemporalBackend(...)) first.",
    );
  }
  return configuredBackend;
}

export async function start(options: WorkflowStartOptions): Promise<WorkflowHandle> {
  return requireBackend().start(options);
}

export async function signal(options: WorkflowSignalOptions): Promise<void> {
  return requireBackend().signal(options);
}

export const workflows = { use, start, signal };
