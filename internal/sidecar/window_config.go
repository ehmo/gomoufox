package sidecar

// Generated window dimensions override context viewport sizing inside Camoufox.
// Leave them native unless the caller explicitly requests a window or fingerprint.
var windowDimensionKeys = []string{
	"window.innerWidth", "window.innerHeight", "window.outerWidth", "window.outerHeight",
	"document.body.clientWidth", "document.body.clientHeight",
}

const pythonNativeWindowScript = `
def native_window_keys(launch_kwargs):
    if not launch_kwargs.pop("_native_window", False):
        return ()
    explicit = launch_kwargs.get("config") or {}
    return tuple(key for key in (
        "window.innerWidth", "window.innerHeight", "window.outerWidth", "window.outerHeight",
        "document.body.clientWidth", "document.body.clientHeight",
    ) if key not in explicit)

def apply_native_window(config, keys):
    if not keys:
        return
    env = config.get("env") or {}
    chunks = sorted((int(k.rsplit("_", 1)[1]), k, v) for k, v in env.items() if k.startswith("CAMOU_CONFIG_"))
    if not chunks:
        return
    fingerprint = orjson.loads("".join(v for _, _, v in chunks))
    for key in keys:
        fingerprint.pop(key, None)
    for _, key, _ in chunks:
        del env[key]
    encoded = orjson.dumps(fingerprint).decode()
    for offset in range(0, len(encoded), 2048):
        env["CAMOU_CONFIG_" + str(offset // 2048 + 1)] = encoded[offset:offset + 2048]
`
