package gomoufox

import (
	"os/exec"
	"testing"
)

func TestManagedPythonToolingResolvesPublishedEnvironments(t *testing.T) {
	script := `
import os,pathlib,runpy,tempfile
from scripts import managed_runtime as runtime
with tempfile.TemporaryDirectory() as home:
    os.environ['HOME']=home
    os.environ['XDG_CACHE_HOME']=home+'/xdg'
    os.environ['LOCALAPPDATA']=home+'/local'
    os.environ.pop('PYTHON',None)
    cache=runtime.default_cache()
    assert runtime.selected_python()=='python3'
    assert not cache.exists()
    relative='Scripts/python.exe' if runtime.sys.platform=='win32' else 'bin/python'
    legacy=cache/relative
    legacy.parent.mkdir(parents=True)
    legacy.write_text('legacy');legacy.chmod(0o700)
    assert runtime.selected_python()==str(legacy)
    assert runtime.cache_from_environment(cache)==cache
    parent=cache/'python-envs'
    env=parent/'env-selected'
    python=env/relative
    python.parent.mkdir(parents=True)
    python.write_text('selected');python.chmod(0o700)
    marker=cache/runtime.MARKER
    marker.write_text('env-selected\n')
    before={str(p):(p.stat().st_size,p.stat().st_mtime_ns) for p in cache.rglob('*')}
    for source in ['scripts/benchmark-realpass.py','scripts/fingerprint-audit.py']:
        select=runpy.run_path(source)['selected_python']
        assert select('')==str(python)
        assert select('/explicit/python')=='/explicit/python'
    assert runtime.cache_from_environment(env)==cache
    assert runtime.active_environment(cache)==env
    after={str(p):(p.stat().st_size,p.stat().st_mtime_ns) for p in cache.rglob('*')}
    assert before==after
    for value in ['', '../env-outside', 'env-/outside', 'env-\\outside', 'env-'+('x'*1024), 'env-missing']:
        marker.write_text(value)
        try: runtime.selected_python()
        except (ValueError,OSError): pass
        else: raise AssertionError('invalid marker fell back to legacy Python')
    marker.write_text('env-selected')
    moved=parent.with_name('old-envs')
    parent.rename(moved);parent.symlink_to(moved,target_is_directory=True)
    try: runtime.selected_python()
    except ValueError: pass
    else: raise AssertionError('symlink environment parent accepted')
    parent.unlink();moved.rename(parent)
    marker.unlink()
    try: runtime.cache_from_environment(env)
    except ValueError: pass
    else: raise AssertionError('unpublished environment selected')
    assert runtime.selected_python()==str(legacy)
`
	if out, err := exec.Command("python3", "-B", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("managed Python tooling: %v\n%s", err, out)
	}
}
