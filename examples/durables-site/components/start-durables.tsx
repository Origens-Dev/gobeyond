import { postAction } from "@go-beyond/react/browser";
import { useState } from "react";

type Status = "idle" | "pending" | "started" | "error";

export function StartDurables() {
  const [echoStatus, setEchoStatus] = useState<Status>("idle");
  const [demoStatus, setDemoStatus] = useState<Status>("idle");
  const [message, setMessage] = useState("");

  return (
    <div>
      <button
        type="button"
        disabled={echoStatus === "pending"}
        onClick={async () => {
          const root = document.querySelector<HTMLElement>("#__gobeyond");
          const buildId = root?.dataset.gobeyondBuild;
          const routeId = root?.dataset.gobeyondRoute;
          if (!buildId || !routeId) {
            throw new Error("GoBeyond route data is missing");
          }
          setEchoStatus("pending");
          setMessage("");
          try {
            const result = await postAction(
              `/_gobeyond/builds/${buildId}/actions/${encodeURIComponent(`${routeId}:startEchoOnce`)}`,
              {},
              { buildId },
            );
            const workflowId =
              result &&
              typeof result === "object" &&
              "workflowId" in result &&
              typeof (result as { workflowId: unknown }).workflowId === "string"
                ? (result as { workflowId: string }).workflowId
                : "";
            setEchoStatus("started");
            setMessage(
              workflowId
                ? `Started ${workflowId}. Check Temporal UI.`
                : "Started. Check Temporal UI.",
            );
          } catch (error) {
            setEchoStatus("error");
            setMessage(error instanceof Error ? error.message : "Failed to start");
          }
        }}
      >
        {echoStatus === "pending" ? "Starting…" : "Run echo-once"}
      </button>
      <button
        type="button"
        disabled={demoStatus === "pending"}
        onClick={async () => {
          const root = document.querySelector<HTMLElement>("#__gobeyond");
          const buildId = root?.dataset.gobeyondBuild;
          const routeId = root?.dataset.gobeyondRoute;
          if (!buildId || !routeId) {
            throw new Error("GoBeyond route data is missing");
          }
          setDemoStatus("pending");
          setMessage("");
          try {
            const result = await postAction(
              `/_gobeyond/builds/${buildId}/actions/${encodeURIComponent(`${routeId}:startDemo`)}`,
              {},
              { buildId },
            );
            const workflowId =
              result &&
              typeof result === "object" &&
              "workflowId" in result &&
              typeof (result as { workflowId: unknown }).workflowId === "string"
                ? (result as { workflowId: string }).workflowId
                : "";
            setDemoStatus("started");
            setMessage(
              workflowId
                ? `Started ${workflowId}. Check Temporal UI.`
                : "Started. Check Temporal UI.",
            );
          } catch (error) {
            setDemoStatus("error");
            setMessage(error instanceof Error ? error.message : "Failed to start");
          }
        }}
      >
        {demoStatus === "pending" ? "Starting…" : "Run demo"}
      </button>
      <p aria-live="polite">{message}</p>
    </div>
  );
}
