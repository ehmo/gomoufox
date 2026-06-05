() => {
  const root = globalThis;
  const restore = root.__gomoufoxMCPProbeRestore || {};
  if (restore.querySelector) Document.prototype.querySelector = restore.querySelector;
  if (root.CSS) {
    if (restore.hadCSSEscape) root.CSS.escape = restore.cssEscape;
    else delete root.CSS.escape;
  }
  delete root.__gomoufoxMCPProbe;
  delete root.__gomoufoxMCPProbeRestore;
  return true;
}
