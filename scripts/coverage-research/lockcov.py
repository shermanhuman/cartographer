# For each repo's mix.lock at origin/main: hex deps, those whose exact
# version is in the Hex cache, and git deps.
import os, re, subprocess, sys
cache = os.path.expanduser('~/.hex/packages/hexpm')
have = set(os.listdir(cache)) if os.path.isdir(cache) else set()
for repo, path in [(a, b) for a, b in (x.split('=') for x in sys.argv[1:])]:
    try:
        lock = subprocess.run(['git', '-C', os.path.expanduser('~/github/' + repo), 'show', 'origin/main:' + path], capture_output=True, text=True, check=True).stdout
    except subprocess.CalledProcessError:
        print(repo, path, 'no lock'); continue
    hexes = re.findall(r'\{:hex, :([a-z_0-9]+), "([^"]+)"', lock)
    gits = re.findall(r'"([a-z_0-9]+)": \{:git,', lock)
    missing = [f'{n}-{v}' for n, v in hexes if f'{n}-{v}.tar' not in have]
    print(f'{repo}:{path}: hex {len(hexes)}, cached {len(hexes)-len(missing)}, git {len(gits)}; missing: {", ".join(missing[:8])}')
