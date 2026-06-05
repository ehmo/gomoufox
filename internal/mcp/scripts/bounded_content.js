({selector, maxBytes, includeHTML, includeText}) => {
  const root = selector ? document.querySelector(selector) : document.documentElement;
  if (!root) return {ok: false, url: location.href, html: "", text: "", message: "selector not found", truncated: false};
  const wantHTML = includeHTML !== false;
  const wantText = includeText !== false;
  const limit = Math.max(0, Number(maxBytes) || 0);
  const encoder = new TextEncoder();
  const decoder = new TextDecoder();
  const escape = (value) => String(value).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  const escapeAttr = (value) => escape(value).replace(/"/g, "&quot;");
  const push = (state, value) => {
    if (state.truncated || value === "") return !state.truncated;
    const bytes = encoder.encode(String(value));
    if (limit > 0 && state.bytes + bytes.byteLength > limit) {
      const keep = Math.max(0, limit - state.bytes);
      if (keep > 0) {
        state.parts.push(decoder.decode(bytes.slice(0, keep)));
        state.bytes += keep;
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
  const walkHTML = (node) => {
    if (htmlState.truncated || !node) return !htmlState.truncated;
    if (node.nodeType === 3) return push(htmlState, escape(node.nodeValue || ""));
    if (node.nodeType !== 1) return true;
    const tag = node.tagName.toLowerCase();
    let open = "<" + tag;
    for (const attr of Array.from(node.attributes || [])) open += " " + attr.name + "=\"" + escapeAttr(attr.value) + "\"";
    open += ">";
    if (!push(htmlState, open)) return false;
    for (let child = node.firstChild; child; child = child.nextSibling) {
      if (!walkHTML(child)) return false;
    }
    return push(htmlState, "</" + tag + ">");
  };
  if (wantHTML) walkHTML(root);
  if (wantText) {
    const walker = document.createTreeWalker(root, 4);
    while (!textState.truncated) {
      const node = walker.nextNode();
      if (!node) break;
      if (!push(textState, node.nodeValue || "")) break;
    }
  }
  return {
    ok: true,
    url: location.href,
    html: wantHTML ? htmlState.parts.join("") : "",
    text: wantText ? textState.parts.join("") : "",
    truncated: (wantHTML && htmlState.truncated) || (wantText && textState.truncated)
  };
}
