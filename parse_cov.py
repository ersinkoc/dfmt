import re

content = open('D:/Codebox/PROJECTS/DFMT/cover.out').read()
lines = content.strip().split('\n')[1:]

# Parse the raw format: file:startLine.startCol,endLine.endCol count hits
funcs = {}
for line in lines:
    parts = line.rsplit(' ', 2)
    if len(parts) == 3:
        file_range, count_str, hits_str = parts
        count, hits = int(count_str), int(hits_str)
        m = re.match(r'(.+):(\d+)\.(\d+),(\d+)\.(\d+)', file_range)
        if m:
            fp = m.group(1)
            if ('/cmd/' in fp or '/internal/' in fp) and count > 0:
                if fp not in funcs:
                    funcs[fp] = {'count': 0, 'hits': 0}
                funcs[fp]['count'] += count
                funcs[fp]['hits'] += hits

# The -func output format has function-level percentages, need to parse it
# Format: filepath:line:\t\tfuncname\t\t\t\tX.X%
func_lines = []
with open('D:/Codebox/PROJECTS/DFMT/cover.out') as f:
    # This is the raw coverage, not the -func output
    pass

# Actually need to run go tool cover -func to get function-level data
import subprocess
result = subprocess.run(['go', 'tool', 'cover', '-func=cover.out'], 
                       capture_output=True, text=True, cwd='D:/Codebox/PROJECTS/DFMT')
output = result.stdout

funcs = []
for line in output.split('\n'):
    # Match: github.com/ersinkoc/dfmt/internal/foo.go:123:    funcName     50.0%
    m = re.match(r'^(github\.com/ersinkoc/dfmt/(?:cmd|internal)/[^:]+):(\d+):\s+(.+?)\s+\s+([\d.]+)%', line)
    if m:
        path = m.group(1)
        func_name = m.group(3).strip()
        pct = float(m.group(4))
        funcs.append((pct, path, func_name))

funcs.sort(key=lambda x: (x[0], x[1]))
for pct, path, name in funcs[:50]:
    print(f'{pct:.1f}% {path}:{name}')