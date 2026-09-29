#!/usr/bin/env bash
set -euo pipefail

# Moves the published modules - the agent at the repository root and every
# plugin/<name> module - to major version N. From v2 on, Go's semantic import
# versioning puts the major version in the module path, so each of those module
# paths gains the /vN suffix and every import, doc reference and replace
# directive follows it. Every requirement on a module of this repository is set
# to vN.0.0, which the replace directives resolve locally until a release tags
# it.
#
# The test modules are never published, but they use the agent's internal
# packages, which Go lets only code under the agent's own path import, so their
# paths sit under the root module's path and follow its major version. The
# example module stays outside it on purpose: like an application, it can build
# against the public API only. Web URLs of the repository are not import paths
# and stay, as do the files that name the old paths on purpose.
#
# Usage: scripts/set-major-version.sh N
#        scripts/set-major-version.sh --check
#
# Running it again with the same N changes nothing. --check changes nothing
# either. CI runs it to fail a tree where running this with the root module's
# own major version would change a reference, or where the requirements between
# the modules disagree on their version or lack a replace directive to the
# module's directory. The builds in this repository resolve each other through
# those replace directives and pass either way; a user's build, which ignores
# them, is the one that would break.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_PATH="github.com/pinpoint-apm/pinpoint-go-agent"
# The changelog and a migration guide name the old paths on purpose, and the
# scripts that work module paths out from the repository's root path name that.
KEEP_FILES='^(CHANGELOG\.md|doc/migration[^/]*\.md|scripts/(set-major-version|release)\.sh)$'

log() {
	printf '==> %s\n' "$*"
}

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

# The rewrite runs once per file (perl -0777), with the module layout passed in
# the environment. A path is matched when it starts a token or follows
# pkg.go.dev/; a path right after "://" is a web URL of the repository and is
# kept. Package paths here have no dots, so a path ends at the first dot, which
# keeps a trailing full stop out of it and turns a function name such as
# ".../plugin/http.handler" into ".../plugin/http/vN.handler", as the runtime
# will report it. With CHECK set it writes nothing, and records each reference
# it would change, with its line, instead.
read -r -d '' REWRITE <<'PERL' || true
BEGIN {
	$repo = $ENV{REPO_PATH};
	$major = $ENV{MAJOR};
	$check = $ENV{CHECK};
	for (split " ", $ENV{MODULES}) {
		my ($dir, $kind) = split /:/;
		push @bases, [$dir eq "." ? $repo : "$repo/$dir", $dir, $kind];
	}
	# Longest first, so a plugin module claims its paths before the root does.
	@bases = sort { length($b->[0]) <=> length($a->[0]) } @bases;
}

# What an import path of this repository becomes, or undef when it names no
# package here, such as a directory mentioned in prose.
sub moved {
	my ($path) = @_;
	# The root module's major suffix goes first, since the test modules sit
	# under it too.
	(my $plain = $path) =~ s{^\Q$repo\E/v[0-9]+(?=/|$)}{$repo};
	for my $module (@bases) {
		my ($base, $dir, $kind) = @$module;
		next unless $plain eq $base || index($plain, "$base/") == 0;
		my $rest = substr($plain, length $base);
		$rest =~ s{^/v[0-9]+(?=/|$)}{} if $kind eq "published";
		if ($rest ne "") {
			my $pkg = $dir eq "." ? substr($rest, 1) : $dir . $rest;
			my @go = glob("$pkg/*.go");
			return undef unless @go;
		}
		return "$base/v$major$rest" if $kind eq "published";
		return "$repo/v$major/$dir$rest" if $kind eq "nested";
		return "$base$rest";
	}
	return undef;
}

my $before = $_;

s{(?<![A-Za-z0-9_./])((?:https?://)?(?:pkg\.go\.dev/(?:badge/)?)?)(\Q$repo\E(?:/[A-Za-z0-9_-]+)*)(?![A-Za-z0-9_-])}{
	my ($prefix, $path, $at) = ($1, $2, $-[0]);
	my $to = $path;
	if ($prefix !~ m{^https?://$}) {
		$to = moved($path);
		unless (defined $to) {
			push @skipped, "$ARGV: $path";
			$to = $path;
		}
	}
	if ($check && $to ne $path) {
		my $head = substr($_, 0, $at);
		push @found, join("\t", $ARGV, 1 + ($head =~ tr/\n//), "$path should be $to");
	}
	"$prefix$to";
}ge;

# In go.mod, a requirement on a module of this repository moves to vN.0.0.
if (!$check && $ARGV =~ m{(?:^|/)go\.mod$}) {
	my $block = 0;
	my @lines;
	for my $line (split /^/) {
		if ($line =~ /^require\s*\(/) {
			$block = 1;
		} elsif ($block && $line =~ /^\)/) {
			$block = 0;
		} elsif ($line !~ /=>/ && ($block ? $line =~ /^\s+\Q$repo\E/ : $line =~ /^require\s+\Q$repo\E/)) {
			$line =~ s{^(\s*(?:require\s+)?\S+\s+)\S+}{${1}v$major.0.0};
		}
		push @lines, $line;
	}
	$_ = join "", @lines;
}

push @changed, $ARGV if $_ ne $before;

END {
	open my $out, ">", $ENV{CHANGED_LIST} or die "$ENV{CHANGED_LIST}: $!";
	print $out "$_\n" for @changed;
	close $out;
	if ($check) {
		open my $found, ">", $ENV{FOUND_LIST} or die "$ENV{FOUND_LIST}: $!";
		print $found "$_\n" for @found;
		close $found;
	}
	print STDERR "left alone, not a package of this repository: $_\n" for @skipped;
}
PERL

failures=0

# report prints one problem, and under GitHub Actions annotates its file too.
report() {
	local file="$1" line="$2" message="$3"
	failures=$((failures + 1))
	if [ -z "$file" ]; then
		printf 'error: %s\n' "$message" >&2
	else
		printf 'error: %s%s: %s\n' "$file" "${line:+:$line}" "$message" >&2
	fi
	if [ "${GITHUB_ACTIONS:-}" = true ]; then
		printf '::error%s::%s\n' "${file:+ file=$file${line:+,line=$line}}" "$message"
	fi
}

# Prints "path version" for each requirement in go.mod file $1 on a module of
# this repository.
repo_requires() {
	awk -v repo="$REPO_PATH" '
		function ours(p) { return p == repo || index(p, repo "/") == 1 }
		/^require[ \t]*\(/ { block = 1; next }
		block && /^\)/ { block = 0; next }
		block && ours($1) { print $1, $2; next }
		!block && $1 == "require" && ours($2) { print $2, $3 }
	' "$1"
}

# The directory go.mod file $1 replaces module path $2 with, if any.
replace_target() {
	awk -v path="$2" '$1 == "replace" && $2 == path {
		for (i = 3; i < NF; i++) if ($i == "=>") { print $(i + 1); exit }
	}' "$1"
}

# check_requires fails every requirement between the modules that names a
# version the others do not, or one of another major version, or that has no
# replace directive to the directory of the module it names.
check_requires() {
	local gomod path ver rel dir got triples
	triples="$(for gomod in $(git ls-files '*go.mod'); do
		repo_requires "$gomod" | sed "s#^#$gomod #"
	done)"
	REQ_COUNT=0
	REQ_VERSION=none
	[ -n "$triples" ] || return 0
	REQ_VERSION="$(awk '{ n[$3]++ } END { for (v in n) if (n[v] > max) { max = n[v]; best = v }; print best }' <<< "$triples")"
	[[ "$REQ_VERSION" =~ ^v$MAJOR\. ]] ||
		report "" "" "the modules require each other at $REQ_VERSION, which is not a v$MAJOR version"
	while read -r gomod path ver; do
		REQ_COUNT=$((REQ_COUNT + 1))
		[ "$ver" = "$REQ_VERSION" ] ||
			report "$gomod" "" "requires $path $ver, where the other modules require $REQ_VERSION"
		rel="$(replace_target "$gomod" "$path")"
		if [ -z "$rel" ]; then
			report "$gomod" "" "requires $path with no replace directive, so builds here would fetch it"
		elif ! dir="$(cd "$(dirname "$gomod")/$rel" 2>/dev/null && pwd)" || [ ! -f "$dir/go.mod" ]; then
			report "$gomod" "" "replaces $path with $rel, which holds no module"
		else
			got="$(awk '$1 == "module" { print $2; exit }' "$dir/go.mod")"
			[ "$got" = "$path" ] || report "$gomod" "" "replaces $path with $rel, which is $got"
		fi
	done <<< "$triples"
}

CHECK=0
case "${1:-}" in
	--check)
		[ $# -eq 1 ] || die "usage: $0 N | --check"
		CHECK=1
		;;
	*)
		[ $# -eq 1 ] || die "usage: $0 N | --check"
		MAJOR="$1"
		[[ "$MAJOR" =~ ^[0-9]+$ ]] && [ "$MAJOR" -ge 2 ] || die "N must be a major version of 2 or more, got '$MAJOR'"
		command -v go >/dev/null 2>&1 || die "go is required"
		;;
esac
command -v perl >/dev/null 2>&1 || die "perl is required"

cd "$ROOT_DIR"

if [ "$CHECK" = 1 ]; then
	# The major version to check against is the root module's own.
	root_path="$(awk '$1 == "module" { print $2; exit }' go.mod)"
	suffix='/v([0-9]+)$'
	[[ "$root_path" =~ $suffix ]] || die "go.mod declares $root_path, which carries no major version to check against"
	MAJOR="${BASH_REMATCH[1]}"
fi

# Each module directory, marked with how its path moves: a published module
# gains the suffix, a nested one sits under the root module's path, and a
# standalone one keeps its path.
modules=()
while IFS= read -r gomod; do
	dir="$(dirname "$gomod")"
	case "$dir" in
		. | plugin/*) modules+=("$dir:published") ;;
		test/*) modules+=("$dir:nested") ;;
		*) modules+=("$dir:standalone") ;;
	esac
done < <(git ls-files '*go.mod')

files=()
while IFS= read -r file; do
	[[ "$file" =~ $KEEP_FILES ]] && continue
	files+=("$file")
done < <(git grep -l -F "$REPO_PATH" -- '*.go' '*go.mod' '*.md' '*.sh' '*.yml' '*.yaml')

[ ${#files[@]} -gt 0 ] || die "no file references $REPO_PATH"

changed_list="$(mktemp)"
found_list="$(mktemp)"
trap 'rm -f "$changed_list" "$found_list"' EXIT

if [ "$CHECK" = 1 ]; then
	REPO_PATH="$REPO_PATH" MAJOR="$MAJOR" MODULES="${modules[*]}" CHECK=1 \
		CHANGED_LIST="$changed_list" FOUND_LIST="$found_list" \
		perl -0777 -ne "$REWRITE" "${files[@]}"
	while IFS=$'\t' read -r file line message; do
		report "$file" "$line" "$message"
	done < "$found_list"
	check_requires
	[ "$failures" -eq 0 ] ||
		die "the tree disagrees with v$MAJOR in $failures places; scripts/set-major-version.sh $MAJOR rewrites the paths, and scripts/release.sh prepare sets the requirements"
	log "${#files[@]} files reference v$MAJOR module paths only, and $REQ_COUNT in-repo requirements agree on $REQ_VERSION"
	exit 0
fi

log "moving ${#modules[@]} modules' references to v$MAJOR in ${#files[@]} files"
REPO_PATH="$REPO_PATH" MAJOR="$MAJOR" MODULES="${modules[*]}" CHANGED_LIST="$changed_list" \
	perl -i -0777 -pe "$REWRITE" "${files[@]}"

# A renamed import can sort differently, and so can a renamed requirement.
go_files=()
while IFS= read -r file; do
	case "$file" in
		*.go) go_files+=("$file") ;;
		go.mod | */go.mod) (cd "$(dirname "$file")" && go mod edit -fmt) ;;
	esac
done < "$changed_list"
if [ ${#go_files[@]} -gt 0 ]; then
	gofmt -w "${go_files[@]}"
fi

log "$(wc -l < "$changed_list" | tr -d ' ') files changed"
