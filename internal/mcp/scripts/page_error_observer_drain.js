({clear, max}) => {
  const errors = Array.isArray(globalThis.__gomoufoxMCPPageErrors) ? globalThis.__gomoufoxMCPPageErrors : [];
  const limit = Math.max(0, Number(max) || 0);
  const out = limit > 0 ? errors.slice(-limit) : errors.slice();
  if (clear) globalThis.__gomoufoxMCPPageErrors = [];
  return {errors: out, dropped: Number(globalThis.__gomoufoxMCPPageErrorsDropped) || 0};
}
