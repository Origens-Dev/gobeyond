import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const expectedDirectory = new URL("../expected/", import.meta.url);
const responses = JSON.parse(
  await readFile(new URL("responses.json", expectedDirectory), "utf8"),
);

function attribute(html, tagPattern, name) {
  const tag = html.match(tagPattern)?.[0];
  return tag?.match(new RegExp(`${name}=["']([^"']+)["']`, "i"))?.[1];
}

function jsonLdDocuments(html) {
  return [...html.matchAll(/<script\s+type=["']application\/ld\+json["'][^>]*>([\s\S]*?)<\/script>/gi)].map(
    (match) => JSON.parse(match[1]),
  );
}

for (const response of responses) {
  test(`${response.route} has correct no-JavaScript HTTP semantics`, async () => {
    assert.ok(response.status >= 200 && response.status < 500);
    assert.ok(response.cacheControl);

    if (response.status >= 300 && response.status < 400) {
      assert.match(response.location, /^\//);
      return;
    }

    const html = await readFile(new URL(response.file, expectedDirectory), "utf8");
    assert.match(html, /^<!doctype html>/i);
    assert.match(html, /<main\b[^>]*>[\s\S]*<h1\b/i);
    assert.doesNotMatch(html, /<main\b[^>]*>\s*<\/main>/i);

    if (response.status === 404 || response.route === "/account") {
      assert.match(html, /<meta\s+name=["']robots["']\s+content=["']noindex,\s*nofollow["']/i);
      assert.equal(response.cacheControl, "private, no-store");
      return;
    }

    assert.equal(response.status, 200);
    assert.match(html, /<title>[^<]+<\/title>/i);
    assert.match(html, /<meta\s+name=["']description["']\s+content=["'][^"']+["']/i);
    assert.match(html, /<meta\s+name=["']robots["']\s+content=["']index,\s*follow["']/i);
    const canonical = attribute(html, /<link\s+rel=["']canonical["'][^>]*>/i, "href");
    assert.match(canonical, /^https:\/\/example\.gobeyond\.dev\//);
  });
}

test("article, product, and location expose valid structured data", async () => {
  for (const file of ["article.html", "product.html", "location.html"]) {
    const html = await readFile(new URL(file, expectedDirectory), "utf8");
    const documents = jsonLdDocuments(html);
    assert.equal(documents.length, 1);
    assert.equal(documents[0]["@context"], "https://schema.org");
    assert.ok(documents[0]["@type"]);
  }
});

test("category pagination and primary content use crawlable links", async () => {
  const html = await readFile(new URL("category.html", expectedDirectory), "utf8");
  assert.match(html, /<a\s+href=["']\/articles\/portable-react["']/i);
  assert.match(html, /<a\s+href=["']\/category\/1["']>Previous page<\/a>/i);
  assert.match(html, /<a\s+href=["']\/category\/3["']>Next page<\/a>/i);
});

test("localized documents use distinct language URLs and reciprocal alternates", async () => {
  const english = await readFile(new URL("en-article.html", expectedDirectory), "utf8");
  const french = await readFile(new URL("fr-article.html", expectedDirectory), "utf8");
  assert.match(english, /<html\s+lang=["']en["']/i);
  assert.match(french, /<html\s+lang=["']fr["']/i);
  for (const html of [english, french]) {
    assert.match(html, /hreflang=["']en["']/i);
    assert.match(html, /hreflang=["']fr["']/i);
  }
});

test("image and location fallbacks remain useful without JavaScript", async () => {
  const product = await readFile(new URL("product.html", expectedDirectory), "utf8");
  assert.match(product, /<img\s+src=["']https:\/\/[^"']+["']\s+alt=["'][^"']+["']/i);
  const location = await readFile(new URL("location.html", expectedDirectory), "utf8");
  assert.match(location, /<address>[\s\S]*<a\s+href=["']tel:/i);
  assert.match(location, />Open GoBeyond Seattle in maps<\/a>/i);
});
