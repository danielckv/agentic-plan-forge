import { describe, expect, it } from "vitest";

import { parseFrame, splitFrames } from "./sse";

describe("splitFrames", () => {
  it("returns complete frames and keeps the remainder", () => {
    const { frames, rest } = splitFrames("data: a\n\ndata: b\n\ndata: par");
    expect(frames).toEqual(["data: a", "data: b"]);
    expect(rest).toBe("data: par");
  });

  it("reassembles a frame split across chunks", () => {
    // A chunk boundary has nothing to do with a frame boundary, so the parser
    // must carry the partial frame forward rather than drop or emit it.
    const first = splitFrames("event: snapshot\ndata: {\"a\":");
    expect(first.frames).toEqual([]);
    const second = splitFrames(first.rest + '1}\n\n');
    expect(second.frames).toHaveLength(1);
    expect(parseFrame(second.frames[0] ?? "")?.data).toBe('{"a":1}');
  });

  it("accepts CRLF line endings", () => {
    const { frames } = splitFrames("data: a\r\n\r\n");
    expect(frames).toEqual(["data: a"]);
  });

  it("drops empty frames from repeated separators", () => {
    const { frames } = splitFrames("data: a\n\n\n\ndata: b\n\n");
    expect(frames).toEqual(["data: a", "data: b"]);
  });
});

describe("parseFrame", () => {
  it("reads id, event and data", () => {
    expect(parseFrame("id: 7\nevent: snapshot\ndata: hello")).toEqual({
      id: "7",
      event: "snapshot",
      data: "hello",
    });
  });

  it("joins multiple data lines with newlines", () => {
    // The server prefixes every line, because a bare newline inside a payload
    // would otherwise end the event early.
    expect(parseFrame("data: a\ndata: b\ndata: c")?.data).toBe("a\nb\nc");
  });

  it("treats a comment-only frame as a heartbeat", () => {
    expect(parseFrame(":")).toBeNull();
    expect(parseFrame(": keep-alive")).toBeNull();
  });

  it("defaults a frame with no event name to message", () => {
    expect(parseFrame("data: x")?.event).toBe("message");
  });

  it("strips exactly one space after the colon", () => {
    expect(parseFrame("data:  padded")?.data).toBe(" padded");
    expect(parseFrame("data:tight")?.data).toBe("tight");
  });

  it("ignores fields it does not know", () => {
    const msg = parseFrame("retry: 500\ndata: x\nnonsense: y");
    expect(msg).toEqual({ id: "", event: "message", data: "x" });
  });
});
