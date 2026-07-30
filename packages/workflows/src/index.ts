/**
 * Portable durable client. Requires a configured World (e.g. @origens-dev/temporal).
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
 * A World executes trigger operations and wakes workers when needed.
 * Hosted Worlds call platform APIs; local Worlds talk to Docker Temporal.
 */
export type WorkflowWorld = {
  start(options: WorkflowStartOptions): Promise<WorkflowHandle>;
  signal(options: WorkflowSignalOptions): Promise<void>;
};

let configuredWorld: WorkflowWorld | undefined;

export function use(world: WorkflowWorld): void {
  configuredWorld = world;
}

function requireWorld(): WorkflowWorld {
  if (!configuredWorld) {
    throw new Error(
      "@go-beyond/workflows: no World configured. Call workflows.use(createTemporalWorld(...)) first.",
    );
  }
  return configuredWorld;
}

export async function start(options: WorkflowStartOptions): Promise<WorkflowHandle> {
  return requireWorld().start(options);
}

export async function signal(options: WorkflowSignalOptions): Promise<void> {
  return requireWorld().signal(options);
}

export const workflows = { use, start, signal };
