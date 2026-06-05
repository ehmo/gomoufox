() => {
  const hostFlag = globalThis.__gomoufoxMCPProbe === true;
  let queryPatched = false;
  try {
    const node = document.querySelector("html");
    queryPatched = !node || node.tagName === "GOMOUFOX_PATCHED";
  } catch (_) {
    queryPatched = true;
  }
  let cssPatched = false;
  try {
    cssPatched = !!(globalThis.CSS && globalThis.CSS.escape && globalThis.CSS.escape("a b") === "gomoufox-patched");
  } catch (_) {
    cssPatched = true;
  }
  return {ok: !hostFlag && !queryPatched && !cssPatched, hostFlag, queryPatched, cssPatched};
}
