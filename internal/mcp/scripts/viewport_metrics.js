() => {
  const doc = document.documentElement;
  const body = document.body;
  const visual = window.visualViewport;
  const width = Math.max(
    window.innerWidth || 0,
    visual ? visual.width || 0 : 0,
    doc ? doc.clientWidth || 0 : 0,
    body ? body.clientWidth || 0 : 0
  );
  const height = Math.max(
    window.innerHeight || 0,
    visual ? visual.height || 0 : 0,
    doc ? doc.clientHeight || 0 : 0,
    body ? body.clientHeight || 0 : 0
  );
  return {width: Math.ceil(width), height: Math.ceil(height)};
}
