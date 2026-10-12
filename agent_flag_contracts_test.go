package gomoufox

import (
	"os/exec"
	"testing"
)

func TestCLIFlagContractsRejectStaleExamplesAndTables(t *testing.T) {
	const check = `
import json, runpy
from pathlib import Path
checker = runpy.run_path('scripts/check-agent-contracts.py')
manifest = json.loads(Path('docs/agent-contracts/cli-flags.json').read_text())
valid = [
    'gomoufox get https://example.com --text --max-bytes 60000 --timeout 25s',
    "gomoufox --json eval https://example.com --width 390 --enable-eval --script 'arg => arg.selector' --arg '{\"selector\":\"h1\"}'",
    'gomoufox fetch https://example.com --form-file image=upload.bin --method post',
    'gomoufox mcp --transport http --auth-token "$TOKEN"',
    'gomoufox skills show core --version 0.1.1',
    'gomoufox session import --file state.json --out profile',
    'gomoufox screenshot https://example.com --out page.png --cookies-file state.json',
    'gomoufox eval https://example.com --enable-eval --script "() => 1" --cookies-file state.json',
]
invalid = [
    'gomoufox screenshot https://example.com --output image.png',
    'gomoufox get https://example.com --format text',
    'gomoufox get https://example.com --timeout 25',
    'gomoufox get https://example.com --max-bytes 600000',
    'gomoufox mcp --max-sessions 99',
    'gomoufox get https://example.com --wait-load-state complete',
    'gomoufox get https://example.com --width wide',
    'gomoufox get https://example.com --max-bytes',
    'gomoufox absent --json',
    'gomoufox skills absent --json',
]
for example in valid:
    errors = checker['check_cli_example'](example, manifest)
    assert not errors, (example, errors)
for example in invalid:
    assert checker['check_cli_example'](example, manifest), example
text = '### '+chr(96)+'gomoufox get'+chr(96)+'\n\n'+checker['flag_table'](manifest['commands']['get']['flags'])
assert checker['update_cli_flag_tables'](text, manifest) == text
corrupt = text.replace('| '+chr(96)+'--max-bytes'+chr(96)+' | integer |', '| '+chr(96)+'--max-bytes'+chr(96)+' | string |', 1)
assert corrupt != text
assert checker['update_cli_flag_tables'](corrupt, manifest) != corrupt
assert not checker['check_cli_examples'](text, manifest)
for name in ('gomoufox', 'gomoufox-mcp'):
    assert not checker['check_cli_examples'](Path('skills', name, 'SKILL.md').read_text(), manifest)
`
	out, err := exec.Command("python3", "-c", check).CombinedOutput()
	if err != nil {
		t.Fatalf("CLI contract validation: %v\n%s", err, out)
	}
}
