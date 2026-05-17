/**
 * Tests for exported helper functions.
 */

import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { extractUserText, truncate } from "../index.js";

describe("truncate", () => {
  it("returns string unchanged when under max", () => {
    assert.equal(truncate("hello", 10), "hello");
  });

  it("returns string unchanged when equal to max", () => {
    assert.equal(truncate("hello", 5), "hello");
  });

  it("truncates and adds ellipsis when over max", () => {
    const result = truncate("hello world this is long", 10);
    assert.ok(result.endsWith("..."));
    assert.ok(result.length <= 13); // 10 + "..."
  });

  it("does not break surrogate pairs", () => {
    // Emoji is a surrogate pair in UTF-16
    const result = truncate("a😀b", 2);
    // Should not cut in the middle of the emoji
    assert.equal(result, "a...");
  });

  it("handles null/undefined gracefully", () => {
    assert.equal(truncate(null, 10), null);
    assert.equal(truncate(undefined, 10), undefined);
  });

  it("handles empty string", () => {
    assert.equal(truncate("", 10), "");
  });
});

describe("extractUserText", () => {
  it("extracts last user message content", () => {
    const messages = [
      { role: "system", content: "You are a bot" },
      { role: "user", content: "Hello" },
      { role: "assistant", content: "Hi!" },
      { role: "user", content: "What is dark mode?" },
    ];
    assert.equal(extractUserText(messages), "What is dark mode?");
  });

  it("handles array content blocks", () => {
    const messages = [
      {
        role: "user",
        content: [
          { type: "text", text: "Show me the " },
          { type: "text", text: "weather in Boston" },
          { type: "image", image_url: { url: "http://..." } },
        ],
      },
    ];
    assert.equal(extractUserText(messages), "Show me the weather in Boston");
  });

  it("returns empty string for no user messages", () => {
    assert.equal(extractUserText([]), "");
  });

  it("returns empty string for null/undefined input", () => {
    assert.equal(extractUserText(null), "");
    assert.equal(extractUserText(undefined), "");
  });

  it("returns empty string when user content is empty", () => {
    const messages = [
      { role: "user", content: "" },
    ];
    assert.equal(extractUserText(messages), "");
  });

  it("returns trimmed content", () => {
    const messages = [
      { role: "user", content: "  hello world  " },
    ];
    assert.equal(extractUserText(messages), "hello world");
  });

  it("handles non-text blocks gracefully", () => {
    const messages = [
      {
        role: "user",
        content: [
          { type: "image", image_url: { url: "http://..." } },
          { type: "text", text: "describe this image" },
        ],
      },
    ];
    assert.equal(extractUserText(messages), "describe this image");
  });
});
