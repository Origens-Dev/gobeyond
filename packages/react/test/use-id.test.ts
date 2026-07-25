import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createUseIdSequence, useId } from "../dist/use-id.js";

test("a baked useId is returned verbatim and falls back to React otherwise", () => {
  const rendered: string[] = [];
  function Field({ baked }: { baked?: string }) {
    rendered.push(useId(baked));
    return createElement("input");
  }
  renderToStaticMarkup(
    createElement(
      "form",
      null,
      createElement(Field, { baked: "gb-root-0" }),
      createElement(Field, {}),
    ),
  );
  assert.equal(rendered[0], "gb-root-0");
  assert.ok((rendered[1] ?? "").length > 0);
  assert.notEqual(rendered[1], "gb-root-0");
});

test("a useId sequence keeps each instance on the id it claimed", () => {
  const useSequencedId = createUseIdSequence(["gb-root-0", "gb-root-1"]);
  const rendered: string[] = [];
  function Logo() {
    rendered.push(useSequencedId());
    return createElement("svg");
  }
  const tree = () =>
    createElement("header", null, createElement(Logo), createElement(Logo));

  renderToStaticMarkup(tree());
  assert.deepEqual(rendered, ["gb-root-0", "gb-root-1"]);

  // Re-rendering must not advance the sequence: instances hold their ids for
  // the life of the page, so hydrated markup keeps matching the Go plan.
  renderToStaticMarkup(tree());
  assert.deepEqual(rendered.slice(2), ["gb-root-0", "gb-root-1"]);
});

test("a useId sequence gives extra instances unique React ids", () => {
  const useSequencedId = createUseIdSequence(["gb-root-0"]);
  const rendered: string[] = [];
  function Logo() {
    rendered.push(useSequencedId());
    return createElement("svg");
  }
  renderToStaticMarkup(
    createElement("header", null, createElement(Logo), createElement(Logo)),
  );
  assert.equal(rendered[0], "gb-root-0");
  assert.ok((rendered[1] ?? "").length > 0);
  assert.notEqual(rendered[1], rendered[0]);
});
