() => {
  const doc = document.documentElement;
  const body = document.body;
  const width = Math.max(
    window.innerWidth || 0,
    doc ? doc.scrollWidth || 0 : 0,
    doc ? doc.offsetWidth || 0 : 0,
    body ? body.scrollWidth || 0 : 0,
    body ? body.offsetWidth || 0 : 0
  );
  const height = Math.max(
    window.innerHeight || 0,
    doc ? doc.scrollHeight || 0 : 0,
    doc ? doc.offsetHeight || 0 : 0,
    body ? body.scrollHeight || 0 : 0,
    body ? body.offsetHeight || 0 : 0
  );
  return {width, height};
}
