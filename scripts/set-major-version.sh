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
# The example and test modules are never published, so their own module paths
# stay and only what they import moves. Web URLs of the repository are not
# import paths and stay too, as do the files that name the old paths on purpose.
#
# Usage: scripts/set-major-version.sh N
#
# Running it again with the same N changes nothing.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_PATH="github.com/pinpoint-apm/pinpoint-go-agent"
SELF="scripts/$(basename "${BASH_SOURCE[0]}")"
# The changelog and a migration guide name the old paths on purpose.
KEEP_FILES='^(CHANGELOG\.md|doc/migration[^/]*\.md)$'

log() {
	printf '==> %s\n' "$*"
}

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

# The rewrite runs once per file (perl -0777 -p), with the module layout passed
# in the environment. A path is matched when it starts a token or follows
# pkg.go.dev/; a path right after "://" is a web URL of the repository and is
# kept. Package paths here have no dots, so a path ends at the first dot, which
# keeps a trailing full stop out of it and turns a function name such as
# ".../plugin/http.handler" into ".../plugin/http/vN.handler", as the runtime
# will report it.
read -r -d '' REWRITE <<'PERL' || true
BEGIN {
	$repo = $ENV{REPO_PATH};
	$major = $ENV{MAJOR};
	for (split " ", $ENV{MODULES}) {
		my ($dir, $published) = split /:/;
		push @bases, [$dir eq "." ? $repo : "$repo/$dir", $dir, $published];
	}
	# Longest first, so a plugin module claims its paths before the root does.
	@bases = sort { length($b->[0]) <=> length($a->[0]) } @bases;
}

# What an import path of this repository becomes, or undef when it names no
# package here, such as a directory mentioned in prose.
sub moved {
	my ($path) = @_;
	for my $module (@bases) {
		my ($base, $dir, $published) = @$module;
		next unless $path eq $base || index($path, "$base/") == 0;
		my $rest = substr($path, length $base);
		$rest =~ s{^/v[0-9]+(?=/|$)}{} if $published;
		if ($rest ne "") {
			my $pkg = $dir eq "." ? substr($rest, 1) : $dir . $rest;
			my @go = glob("$pkg/*.go");
			return undef unless @go;
		}
		return $published ? "$base/v$major$rest" : $path;
	}
	return undef;
}

my $before = $_;

s{(?<![A-Za-z0-9_./])((?:https?://)?(?:pkg\.go\.dev/(?:badge/)?)?)(\Q$repo\E(?:/[A-Za-z0-9_-]+)*)(?![A-Za-z0-9_-])}{
	my ($prefix, $path) = ($1, $2);
	my $to = $path;
	if ($prefix !~ m{^https?://$}) {
		$to = moved($path);
		unless (defined $to) {
			push @skipped, "$ARGV: $path";
			$to = $path;
		}
	}
	"$prefix$to";
}ge;

# In go.mod, a requirement on a module of this repository moves to vN.0.0.
if ($ARGV =~ m{(?:^|/)go\.mod$}) {
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
	print STDERR "left alone, not a package of this repository: $_\n" for @skipped;
}
PERL

[ $# -eq 1 ] || die "usage: $0 N"
MAJOR="$1"
[[ "$MAJOR" =~ ^[0-9]+$ ]] && [ "$MAJOR" -ge 2 ] || die "N must be a major version of 2 or more, got '$MAJOR'"
command -v perl >/dev/null 2>&1 || die "perl is required"
command -v go >/dev/null 2>&1 || die "go is required"

cd "$ROOT_DIR"

# Each module directory, marked with whether it is published.
modules=()
while IFS= read -r gomod; do
	dir="$(dirname "$gomod")"
	case "$dir" in
		. | plugin/*) modules+=("$dir:1") ;;
		*) modules+=("$dir:0") ;;
	esac
done < <(git ls-files '*go.mod')

files=()
while IFS= read -r file; do
	[ "$file" = "$SELF" ] && continue
	[[ "$file" =~ $KEEP_FILES ]] && continue
	files+=("$file")
done < <(git grep -l -F "$REPO_PATH" -- '*.go' '*go.mod' '*.md' '*.sh' '*.yml' '*.yaml')

[ ${#files[@]} -gt 0 ] || die "no file references $REPO_PATH"

changed_list="$(mktemp)"
trap 'rm -f "$changed_list"' EXIT

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
