/**
 * Portable durable client. Call workflows.use(...) with a Temporal (or other)
 * client implementation first — there is no implicit default.
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

/** start/signal implementation (e.g. from @origens-dev/temporal). */
export type WorkflowClient = {
  start(options: WorkflowStartOptions): Promise<WorkflowHandle>;
  signal(options: WorkflowSignalOptions): Promise<void>;
};

let configuredClient: WorkflowClient | undefined;

export function use(client: WorkflowClient): void {
  configuredClient = client;
}

function requireClient(): WorkflowClient {
  if (!configuredClient) {
    throw new Error(
      "@go-beyond/workflows: no client configured. Call workflows.use(createClient(...)) first.",
    );
  }
  return configuredClient;
}

export async function start(options: WorkflowStartOptions): Promise<WorkflowHandle> {
  return requireClient().start(options);
}

export async function signal(options: WorkflowSignalOptions): Promise<void> {
  return requireClient().signal(options);
}

export const workflows = { use, start, signal };
