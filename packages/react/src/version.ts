import { version as reactVersion } from "react";
import { version as reactDOMVersion } from "react-dom";

export const PINNED_REACT_VERSION = "19.2.8" as const;

export function assertPinnedReactVersions(): void {
  if (
    reactVersion !== PINNED_REACT_VERSION ||
    reactDOMVersion !== PINNED_REACT_VERSION
  ) {
    throw new Error(
      `GoBeyond requires react and react-dom ${PINNED_REACT_VERSION}; ` +
        `loaded react ${reactVersion} and react-dom ${reactDOMVersion}.`,
    );
  }
}
