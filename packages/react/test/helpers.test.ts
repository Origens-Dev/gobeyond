import assert from "node:assert/strict";
import test from "node:test";
import { imageSrc } from "../dist/helpers.js";

test("imageSrc builds the default optimizer URL", () => {
  assert.equal(
    imageSrc("/brand/logo mark.png", { w: 640 }),
    "/_gobeyond/image?v=2&url=%2Fbrand%2Flogo+mark.png&w=640&q=75",
  );
});

test("imageSrc includes explicit quality and format", () => {
  assert.equal(
    imageSrc("/photos/hero.jpg", { w: 1200, q: 82, f: "jpeg" }),
    "/_gobeyond/image?v=2&url=%2Fphotos%2Fhero.jpg&w=1200&q=82&f=jpeg",
  );
});

test("imageSrc leaves format negotiation to the image edge by default", () => {
  assert.equal(
    imageSrc("https://images.example.test/hero.jpg", { w: 640 }),
    "/_gobeyond/image?v=2&url=https%3A%2F%2Fimages.example.test%2Fhero.jpg&w=640&q=75",
  );
});

test("imageSrc bypasses legacy browser cache URLs", () => {
  const revised = imageSrc("/same-source.jpg", { w: 640 });
  const legacy = "/_gobeyond/image?url=%2Fsame-source.jpg&w=640&q=75";
  assert.notEqual(revised, legacy);
  assert.equal(new URL(revised, "https://site.test").searchParams.get("v"), "2");
});
