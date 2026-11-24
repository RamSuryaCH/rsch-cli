#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)

check_file() {
	file=$1
	if [ ! -s "$file" ]; then
		printf 'missing or empty agent asset: %s\n' "$file" >&2
		exit 1
	fi
}

check_file "$root/AGENTS.md"
check_file "$root/agent/brief.md"
check_file "$root/agent/rules/trigger.md"
check_file "$root/agent/rules/workflow.md"
check_file "$root/agent/rules/writeback.md"
check_file "$root/agent/skills/getting-started.md"
check_file "$root/agent/skills/repository-inspection.md"
check_file "$root/agent/skills/safe-automation.md"
check_file "$root/agent/skills/verification-and-evidence.md"

paragraphs=$(awk 'BEGIN { RS = ""; count = 0 } NF { count++ } END { print count }' "$root/agent/brief.md")
if [ "$paragraphs" -ne 1 ]; then
	printf 'agent/brief.md must contain exactly one paragraph\n' >&2
	exit 1
fi

for file in "$root"/agent/rules/*.md "$root"/agent/skills/*.md; do
	metadata=$(awk '
		NR == 1 && $0 == "---" { in_frontmatter = 1; next }
		in_frontmatter && $0 == "---" { exit }
		in_frontmatter { print }
	' "$file")
	if ! printf '%s\n' "$metadata" | grep -q '^name: .'; then
		printf 'missing frontmatter name: %s\n' "$file" >&2
		exit 1
	fi
	if ! printf '%s\n' "$metadata" | grep -q '^description: .'; then
		printf 'missing frontmatter description: %s\n' "$file" >&2
		exit 1
	fi
done

printf 'agent assets verified\n'
