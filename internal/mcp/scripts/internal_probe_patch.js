() => {
  const root = globalThis;
  root.__gomoufoxMCPProbeRestore = {
    querySelector: Document.prototype.querySelector,
    cssEscape: root.CSS && root.CSS.escape,
    hadCSSEscape: !!(root.CSS && root.CSS.escape)
  };
  root.__gomoufoxMCPProbe = true;
  Document.prototype.querySelector = () => ({tagName: "GOMOUFOX_PATCHED"});
  if (root.CSS) root.CSS.escape = () => "gomoufox-patched";
  return {
    ok: root.__gomoufoxMCPProbe === true,
    queryTag: document.querySelector("html").tagName,
    css: root.CSS && root.CSS.escape ? root.CSS.escape("a b") : "",
    cssAvailable: !!(root.CSS && root.CSS.escape)
  };
}
