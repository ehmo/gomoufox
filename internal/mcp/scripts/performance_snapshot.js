() => {
  const nav = performance.getEntriesByType("navigation")[0] || {};
  const resources = performance.getEntriesByType("resource") || [];
  const byType = {};
  let transferSize = 0;
  let encodedBodySize = 0;
  for (const item of resources) {
    const key = item.initiatorType || "other";
    byType[key] = (byType[key] || 0) + 1;
    transferSize += Number(item.transferSize) || 0;
    encodedBodySize += Number(item.encodedBodySize) || 0;
  }
  const memory = performance.memory ? {
    js_heap_size_limit: performance.memory.jsHeapSizeLimit || 0,
    total_js_heap_size: performance.memory.totalJSHeapSize || 0,
    used_js_heap_size: performance.memory.usedJSHeapSize || 0
  } : {};
  return {
    url: location.href,
    title: document.title || "",
    navigation: {
      type: nav.type || "",
      dom_content_loaded_ms: Math.max(0, Math.round((nav.domContentLoadedEventEnd || 0) - (nav.startTime || 0))),
      load_event_ms: Math.max(0, Math.round((nav.loadEventEnd || 0) - (nav.startTime || 0))),
      transfer_size: Number(nav.transferSize) || 0,
      encoded_body_size: Number(nav.encodedBodySize) || 0
    },
    resources: {
      count: resources.length,
      by_initiator_type: byType,
      transfer_size: transferSize,
      encoded_body_size: encodedBodySize
    },
    memory,
    viewport: {
      width: window.innerWidth || 0,
      height: window.innerHeight || 0,
      device_pixel_ratio: window.devicePixelRatio || 0
    }
  };
}
