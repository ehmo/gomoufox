() => {
  const root = globalThis;
  if (root.__gomoufoxMCPPageErrorsInstalled) return true;
  root.__gomoufoxMCPPageErrorsInstalled = true;
  root.__gomoufoxMCPPageErrorsDropped = root.__gomoufoxMCPPageErrorsDropped || 0;
  root.__gomoufoxMCPPageErrors = [];
  const push = (type, value) => {
    const message = value && value.message ? String(value.message) : String(value);
    root.__gomoufoxMCPPageErrors.push({type, message: message.length > 2048 ? message.slice(0, 2048) : message});
    if (root.__gomoufoxMCPPageErrors.length > 200) {
      root.__gomoufoxMCPPageErrors.shift();
      root.__gomoufoxMCPPageErrorsDropped++;
    }
  };
  window.addEventListener("error", (event) => push("error", event.error || event.message || ""));
  window.addEventListener("unhandledrejection", (event) => push("unhandledrejection", event.reason || ""));
  return true;
}
