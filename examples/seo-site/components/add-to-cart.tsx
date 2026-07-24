import { fetchWithBuildGuard } from "@go-beyond/react/browser";
import { useState } from "react";

export interface AddToCartProps {
  productName: string;
}

export function AddToCart({ productName }: AddToCartProps) {
  const [added, setAdded] = useState(false);
  const [pending, setPending] = useState(false);

  return (
    <div aria-live="polite">
      <button
        type="button"
        onClick={async () => {
          const root = document.querySelector<HTMLElement>("#__gobeyond");
          const buildId = root?.dataset.gobeyondBuild;
          const routeId = root?.dataset.gobeyondRoute;
          if (!buildId || !routeId) {
            throw new Error("GoBeyond route data is missing");
          }
          setPending(true);
          try {
            const actionId = `${routeId}:addToCart`;
            const response = await fetchWithBuildGuard(
              `/_gobeyond/builds/${buildId}/actions/${encodeURIComponent(actionId)}`,
              {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ productSlug: "trail-pack", quantity: 1 }),
              },
              { buildId },
            );
            if (!response.ok) {
              throw new Error(`Action failed with ${response.status}`);
            }
            setAdded(true);
          } finally {
            setPending(false);
          }
        }}
        disabled={added || pending}
      >
        {added
          ? "Added to cart"
          : pending
            ? "Adding…"
            : `Add ${productName} to cart`}
      </button>
    </div>
  );
}
