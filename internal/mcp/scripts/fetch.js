async ({url, method, headers, body, maxBytes}) => {
  let reader;
  let safeCancel = async () => {};
  try {
    const response = await fetch(url, {method, headers, body: body || undefined, credentials: "include"});
    const getReader = (stream) => stream && stream.getReader ? stream.getReader() : null;
    const read = (reader) => reader.read();
    const cancelReader = (reader) => reader.cancel();
    const headersObject = {};
    if (response.headers && typeof response.headers.forEach === "function") {
      response.headers.forEach((value, key) => { headersObject[key] = value; });
    }
    if (!response.body) return {ok: true, url: response.url, status: response.status, headers: headersObject, body: "", truncated: false};
    reader = getReader(response.body);
    if (!reader) return {ok: false, url, status: response.status, headers: headersObject, body: "", message: "streaming response body is unavailable"};
    safeCancel = async () => { try { await cancelReader(reader); } catch (_) {} };
    const limit = Math.max(0, Number(maxBytes) || 0);
    const chunks = [];
    let total = 0;
    let truncated = false;
    while (true) {
      const item = await read(reader);
      if (item.done) break;
      const chunk = item.value || new Uint8Array();
      if (limit > 0 && total + chunk.byteLength > limit) {
        const keep = Math.max(0, limit - total);
        if (keep > 0) {
          chunks.push(chunk.slice(0, keep));
          total += keep;
        }
        truncated = true;
        await safeCancel();
        break;
      }
      chunks.push(chunk);
      total += chunk.byteLength;
      if (limit > 0 && total >= limit) {
        truncated = true;
        await safeCancel();
        break;
      }
    }
    const bytes = new Uint8Array(total);
    let offset = 0;
    for (const chunk of chunks) {
      bytes.set(chunk, offset);
      offset += chunk.byteLength;
    }
    const text = new TextDecoder().decode(bytes);
    return {ok: true, url: response.url, status: response.status, headers: headersObject, body: text, truncated};
  } catch (error) {
    await safeCancel();
    const message = error && error.message ? String(error.message) : String(error);
    return {ok: false, url, status: 0, headers: {}, body: "", message};
  }
}
