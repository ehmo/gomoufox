async ({url, method, headers, fields, files, inputSelector, maxBytes}) => {
  let reader;
  let safeCancel = async () => {};
  const toBase64 = (bytes) => {
    let binary = "";
    const size = 0x8000;
    for (let offset = 0; offset < bytes.length; offset += size) {
      binary += String.fromCharCode(...bytes.subarray(offset, offset + size));
    }
    return btoa(binary);
  };
  try {
    const input = document.querySelector(inputSelector);
    if (!input || !input.files) {
      return {ok: false, url, status: 0, headers: {}, body_base64: "", message: "file input is unavailable"};
    }
    const form = new FormData();
    for (const field of fields || []) {
      form.append(String(field.name), String(field.value));
    }
    const fileParts = files || [];
    if (input.files.length !== fileParts.length) {
      return {ok: false, url, status: 0, headers: {}, body_base64: "", message: "selected file count mismatch"};
    }
    for (let index = 0; index < fileParts.length; index++) {
      form.append(String(fileParts[index].name), input.files[index]);
    }
    const response = await fetch(url, {method, headers, body: form, credentials: "include"});
    const getReader = (stream) => stream && stream.getReader ? stream.getReader() : null;
    const read = (streamReader) => streamReader.read();
    const cancelReader = (streamReader) => streamReader.cancel();
    const headersObject = {};
    if (response.headers && typeof response.headers.forEach === "function") {
      response.headers.forEach((value, key) => { headersObject[key] = value; });
    }
    if (!response.body) return {ok: true, url: response.url, status: response.status, headers: headersObject, body_base64: "", truncated: false};
    reader = getReader(response.body);
    if (!reader) return {ok: false, url, status: response.status, headers: headersObject, body_base64: "", message: "streaming response body is unavailable"};
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
    return {ok: true, url: response.url, status: response.status, headers: headersObject, body_base64: toBase64(bytes), truncated};
  } catch (error) {
    await safeCancel();
    const message = error && error.message ? String(error.message) : String(error);
    return {ok: false, url, status: 0, headers: {}, body_base64: "", message};
  }
}
