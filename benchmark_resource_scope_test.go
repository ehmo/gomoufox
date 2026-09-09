package gomoufox

import (
	"os/exec"
	"testing"
)

func TestBenchmarkResourceScopeMatchesBothHarnesses(t *testing.T) {
	script := `import copy, runpy
ns = runpy.run_path("scripts/benchmark-realpass.py")
options = ns["options_from_reports"]
current = {"options": {"resource_scope": "harness_process_tree", "resource_sampling": "fixed_period"}}
for key in ("resource_scope", "resource_sampling"):
    assert options(current, current)[key] == current["options"][key]
    assert key not in options({}, {})
    for value in (None, "legacy"):
        other = copy.deepcopy(current)
        if value is None:
            del other["options"][key]
        else:
            other["options"][key] = value
        try:
            options(current, other)
        except SystemExit as exc:
            assert key in str(exc), str(exc)
        else:
            raise AssertionError(f"different {key} values were compared")

benchmark = {
    "mode": "extended", "target_count": 100,
    "options": {"go_sidecar_runtime": "node-direct", "persona_os": "linux",
                "persona_bundle_sha256": "a"*64, "resource_scope": "harness_process_tree",
                "resource_sampling": "fixed_period"},
    "summary": {"go": {"failed": 0}, "python": {"failed": 0},
                "go_only_regression_count": 0, "outcome_mismatch_count": 0,
                "ratios": {"wall_time": 1, "target_duration": 1,
                           "peak_rss": .9, "peak_cpu": .9, "report_tokens": .2}},
}

assert ns["python_removal_readiness"](benchmark)["candidate"]
for key in ("resource_scope", "resource_sampling"):
    legacy = copy.deepcopy(benchmark)
    del legacy["options"][key]
    assert not ns["python_removal_readiness"](legacy)["candidate"], f"legacy {key} evidence qualified"
`
	if out, err := exec.Command("python3", "-B", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("benchmark resource scope: %v\n%s", err, out)
	}
}

func TestPythonResourceMonitorUsesFixedPeriod(t *testing.T) {
	script := `import ast, math, pathlib, threading, types
path = pathlib.Path("scripts/python-realpass.py")
node = next(n for n in ast.parse(path.read_text()).body if isinstance(n, ast.ClassDef) and n.name == "ProcessMonitor")
env = {"threading": threading}
exec(compile(ast.Module(body=[node], type_ignores=[]), str(path), "exec"), env)
Monitor = env["ProcessMonitor"]
for cost in (.1, .45, 1.2):
    clock = types.SimpleNamespace(now=0.0)
    starts = []
    def sample(pid):
        starts.append(clock.now)
        clock.now += cost
        return {"cpu": 1, "rss_kib": 1, "count": 1, "commands": []}
    class Stop:
        def is_set(self):
            return len(starts) >= 3
        def wait(self, delay):
            assert delay >= 0, delay
            clock.now += delay
    env["time"] = types.SimpleNamespace(monotonic=lambda: clock.now)
    env["sample_process_tree"] = sample
    monitor = Monitor(1, .5)
    monitor.stop_event = Stop()
    monitor.run()
    expected = [0.0, .5, 1.0] if cost < .5 else [0.0, cost, 2*cost]
    assert all(math.isclose(a,b) for a,b in zip(starts, expected)), (cost, starts, expected)
    assert monitor.summary["samples"] == 3
    assert not monitor.summary["sample_errors"], monitor.summary
`
	if out, err := exec.Command("python3", "-B", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("Python resource sample cadence: %v\n%s", err, out)
	}
}
