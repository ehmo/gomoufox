({selector, maxBytes, includeHTML, includeText}) => {
  const root = selector ? document.querySelector(selector) : document.documentElement;
  if (!root) return {ok: false, url: location.href, html: "", text: "", message: "selector not found", truncated: false};
  const wantHTML = includeHTML !== false;
  const wantText = includeText !== false;
  const limit = Math.max(0, Number(maxBytes) || 0);
  const encoder = new TextEncoder();
  const escape = (value) => String(value).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  const escapeAttr = (value) => escape(value).replace(/"/g, "&quot;");
  const push = (state, value) => {
    if (state.truncated || value === "") return !state.truncated;
    const bytes = encoder.encode(String(value));
    if (limit > 0 && state.bytes + bytes.byteLength > limit) {
      const remaining = Math.max(0, limit - state.bytes);
      let kept = "", keptBytes = 0;
      for (const char of String(value)) {
        const code = char.codePointAt(0);
        const size = code < 128 ? 1 : code < 2048 ? 2 : code < 65536 ? 3 : 4;
        if (keptBytes + size > remaining) break;
        kept += char;
        keptBytes += size;
      }
      if (kept) {
        state.parts.push(kept);
        state.bytes += keptBytes;
      }
      state.truncated = true;
      return false;
    }
    state.parts.push(String(value));
    state.bytes += bytes.byteLength;
    return true;
  };
  const htmlState = {parts: [], bytes: 0, truncated: false};
  const textState = {parts: [], bytes: 0, truncated: false};
  const pushEscaped = (state, value, attribute) => {
    value = String(value);
    for (let start = 0; start < value.length;) {
      let end = Math.min(start + 4096, value.length);
      if (end < value.length && value.charCodeAt(end - 1) >= 0xd800 && value.charCodeAt(end - 1) <= 0xdbff) end--;
      const part = value.slice(start, end);
      if (!push(state, attribute ? escapeAttr(part) : escape(part))) return false;
      start = end;
    }
    return true;
  };
  let htmlVisited = 0;
  const walkHTML = (node, depth = 0) => {
    if (htmlState.truncated || !node) return !htmlState.truncated;
    if (++htmlVisited > 50000 || depth > 512) { htmlState.truncated = true; return false; }
    if (node.nodeType === 3) return pushEscaped(htmlState, node.nodeValue || "", false);
    if (node.nodeType !== 1) return true;
    const tag = node.tagName.toLowerCase();
    if (!push(htmlState, "<" + tag)) return false;
    for (const attr of node.attributes || []) {
      if (!push(htmlState, " " + attr.name + "=\"") || !pushEscaped(htmlState, attr.value, true) || !push(htmlState, "\"")) return false;
    }
    if (!push(htmlState, ">")) return false;
    for (let child = node.firstChild; child; child = child.nextSibling) {
      if (!walkHTML(child, depth + 1)) return false;
    }
    return push(htmlState, "</" + tag + ">");
  };
  if (wantHTML) walkHTML(root);
  if (wantText) {
    const ignored = new Set(["head", "script", "style", "noscript", "template"]);
    const blocks = new Set(["address", "article", "aside", "blockquote", "dd", "details", "div", "dl", "dt", "fieldset", "figcaption", "figure", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hr", "li", "main", "nav", "ol", "p", "pre", "section", "summary", "table", "tr", "ul"]);
    let space = false, line = false, buffer = "", visited = 0, scanned = 0;
    const scanLimit = Math.max(65536, limit * 8);
    const emit = (value) => {
      for (const char of value) {
        const code = char.codePointAt(0);
        const size = code < 128 ? 1 : code < 2048 ? 2 : code < 65536 ? 3 : 4;
        if (limit > 0 && textState.bytes + size > limit) {
          textState.truncated = true;
          return false;
        }
        buffer += char;
        textState.bytes += size;
        if (buffer.length >= 4096) {
          textState.parts.push(buffer);
          buffer = "";
        }
      }
      return true;
    };
    const walkText = (node, pre, depth) => {
      if (!node || textState.truncated) return;
      if (++visited > 50000 || depth > 512) {
        textState.truncated = true;
        return;
      }
      if (node.nodeType === 3) {
        for (const char of node.nodeValue || "") {
          if (++scanned > scanLimit) { textState.truncated = true; return; }
          if (/\s/u.test(char)) {
            if (pre && (char === "\n" || char === "\r")) line = true;
            else space = true;
            continue;
          }
          if (textState.bytes > 0 && (line || space) && !emit(line ? "\n" : " ")) return;
          line = false;
          space = false;
          if (!emit(char)) return;
        }
        return;
      }
      if (node.nodeType !== 1) return;
      const tag = node.tagName.toLowerCase();
      if (ignored.has(tag) || node.hasAttribute?.("hidden") || node.getAttribute?.("aria-hidden") === "true") return;
      const block = blocks.has(tag);
      line = line || block || tag === "br";
      for (let child = node.firstChild; child && !textState.truncated; child = child.nextSibling) walkText(child, pre || tag === "pre", depth + 1);
      line = line || block;
      space = space || tag === "td" || tag === "th";
    };
    walkText(selector ? root : document.body || root, false, 0);
    if (buffer) textState.parts.push(buffer);
  }
  return {
    ok: true,
    url: location.href,
    html: wantHTML ? htmlState.parts.join("") : "",
    text: wantText ? textState.parts.join("") : "",
    html_truncated: wantHTML && htmlState.truncated,
    text_truncated: wantText && textState.truncated,
    truncated: (wantHTML && htmlState.truncated) || (wantText && textState.truncated)
  };
}
