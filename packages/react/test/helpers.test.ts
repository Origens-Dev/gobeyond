import assert from "node:assert/strict";
import test from "node:test";
import { imageSrc } from "../dist/helpers.js";

test("imageSrc builds the default optimizer URL", () => {
  assert.equal(
    imageSrc("/brand/logo mark.png", { w: 640 }),
    "/_gobeyond/image?url=%2Fbrand%2Flogo+mark.png&w=640&q=75",
  );
});

test("imageSrc includes explicit quality and format", () => {
  assert.equal(
    imageSrc("/photos/hero.jpg", { w: 1200, q: 82, f: "jpeg" }),
    "/_gobeyond/image?url=%2Fphotos%2Fhero.jpg&w=1200&q=82&f=jpeg",
  );
});
