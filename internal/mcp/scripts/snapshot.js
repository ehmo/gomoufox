({max, interactiveOnly, includeValues}) => {
  const MAX_VALUE_LENGTH = __MAX_SNAPSHOT_VALUE_LENGTH__;
  const attr = (el, name) => el.getAttribute(name);
  const tagNameOf = (el) => el.tagName || "";
  const idOf = (el) => el.id || "";
  const valueOf = (el) => {
    const tag = tagNameOf(el).toLowerCase();
    if (tag === "textarea" || tag === "input") return el.value || "";
    return "";
  };
  const childrenOf = (el) => el.children;
  const parentElementOf = (el) => el.parentElement;
  const clientRectsOf = (el) => el.getClientRects();
  const computedStyleOf = (el) => window.getComputedStyle ? window.getComputedStyle(el) : getComputedStyle(el);
  const cssEscape = window.CSS && typeof window.CSS.escape === "function" ? window.CSS.escape.bind(window.CSS) : ((value) => String(value).replace(/[^a-zA-Z0-9_\-]/g, "\\$&"));
  const inputType = (el) => (attr(el, "type") || "").toLowerCase();
  const buttonInputTypes = new Set(["button", "submit", "reset"]);
  const roleFor = (el) => {
    const tag = tagNameOf(el).toLowerCase();
    if (/^h[1-6]$/.test(tag)) return "heading";
    if (tag === "a") return "link";
    if (tag === "button") return "button";
    if (tag === "input" && buttonInputTypes.has(inputType(el))) return "button";
    if (tag === "input" || tag === "textarea") return "textbox";
    if (tag === "select") return "combobox";
    return el.getAttribute("role") || tag || "generic";
  };
  const fieldText = (el) => [
    attr(el, "type"),
    attr(el, "name"),
    attr(el, "id"),
    attr(el, "autocomplete"),
    attr(el, "aria-label"),
    attr(el, "placeholder")
  ].filter(Boolean).join(" ").toLowerCase();
  const isHiddenElement = (el) => {
    const tag = tagNameOf(el).toLowerCase();
    if (tag === "input" && inputType(el) === "hidden") return true;
    if (el.hidden || (attr(el, "aria-hidden") || "").toLowerCase() === "true") return true;
    const style = computedStyleOf(el);
    if (style && (style.display === "none" || style.visibility === "hidden")) return true;
    if (clientRectsOf(el).length === 0) return true;
    return false;
  };
  const looksSensitiveValue = (value) => {
    const compact = String(value || "").trim();
    return /^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(compact) ||
      /^(sk|pk|ghp|gho|github_pat|xox[baprs])[-_]/i.test(compact) ||
      /^[a-f0-9]{32,}$/i.test(compact) ||
      /^[A-Za-z0-9_-]{40,}$/.test(compact);
  };
  const isSensitiveField = (el) => {
    const type = inputType(el);
    if (type === "password" || type === "hidden") return true;
    return /(password|passwd|secret|token|bearer|auth|credential|session|cookie|csrf|xsrf|nonce|otp|totp|mfa|pin|verification[_ -]?code|one[_ -]?time[_ -]?code|jwt|access|refresh|api[_ -]?key|private[_ -]?key)/i.test(fieldText(el));
  };
  const safeValueFor = (el) => {
    if (!includeValues || isHiddenElement(el) || isSensitiveField(el)) return "";
    const value = String(valueOf(el) || "");
    if (value === "" || value.length > MAX_VALUE_LENGTH || looksSensitiveValue(value)) return "";
    return value;
  };
  const nameFor = (el, role) => {
    const tag = tagNameOf(el).toLowerCase();
    const buttonValue = tag === "input" && role === "button" ? valueOf(el) : "";
    return (attr(el, "aria-label") || attr(el, "placeholder") || attr(el, "name") || el.innerText || buttonValue || attr(el, "href") || "").trim();
  };
  const pathFor = (el) => {
    const id = idOf(el);
    if (id) return "#" + cssEscape(id);
    const parts = [];
    for (let node = el; node && node.nodeType === 1 && parts.length < 6; node = parentElementOf(node)) {
      const tag = tagNameOf(node).toLowerCase();
      const parent = parentElementOf(node);
      if (!parent) { parts.unshift(tag); break; }
      const index = Array.from(childrenOf(parent)).filter(child => tagNameOf(child) === tagNameOf(node)).indexOf(node) + 1;
      parts.unshift(tag + ":nth-of-type(" + index + ")");
    }
    return parts.join(" > ");
  };
  const interactive = new Set(["button", "link", "textbox", "combobox"]);
  const nodes = Array.from(document.querySelectorAll("a,button,input,textarea,select,h1,h2,h3,h4,h5,h6,[role]"));
  const out = [];
  for (const el of nodes) {
    if (isHiddenElement(el)) continue;
    const role = roleFor(el);
    if (interactiveOnly && !interactive.has(role)) continue;
    const name = nameFor(el, role);
    if (!name && role !== "textbox") continue;
    const item = {role, name, resolver: pathFor(el)};
    if (role === "heading") item.level = Number(tagNameOf(el).slice(1)) || 0;
    if (role === "link") item.href = attr(el, "href") || "";
    if (role === "textbox") {
      const value = safeValueFor(el);
      if (value) {
        item.value = value;
        item.value_kind = "safe";
      }
    }
    if (el.required) item.required = true;
    out.push(item);
    if (max > 0 && out.length >= max) break;
  }
  return out;
}
