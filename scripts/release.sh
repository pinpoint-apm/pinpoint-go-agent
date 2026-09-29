#!/usr/bin/env bash
set -euo pipefail

# Releases the published modules - the agent at the repository root and every
# plugin/<name> module - together, at one version. Go finds a module in a
# subdirectory by a tag prefixed with that directory (plugin/gin/v2.0.0), and a
# user's build ignores replace directives, so every requirement between these
# modules has to name a version that is tagged. The release commit therefore
# requires every sibling at the release version, and every published module is
# tagged at that commit; the replace directives keep builds inside this
# repository on the local code.
#
# Usage: scripts/release.sh <command> vX.Y.Z[-pre] [-y]
#
#   prepare  set every requirement on a module of this repository, and Version
#            in version.go, to the version: the content of the release commit
#   check    confirm HEAD is that release commit and none of its tags exist,
#            here or on the remote; changes nothing
#   tag      check, then tag every published module at HEAD
#   push     push those tags to the remote in one atomic push; -y skips the
#            confirmation
#   verify   in a module outside the repository, fetch every published module
#            at the version and build it, the way a user's build does
#
# A release runs prepare, then commit and push the change and let CI pass, then
# tag, push and verify. Cut a pre-release (v2.0.0-rc.1) first: the module proxy
# and the checksum database keep the first content they see for a version, so a
# pushed tag cannot be moved, and a broken release is fixed by the next version
# plus a retract directive.
#
# REMOTE names the remote to check and push to (default: origin).

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_PATH="github.com/pinpoint-apm/pinpoint-go-agent"
REMOTE="${REMOTE:-origin}"
# A semantic version without build metadata, which module versions cannot
# carry; numeric identifiers take no leading zero.
NUM='(0|[1-9][0-9]*)'
PRE_ID='(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)'
SEMVER="^v$NUM\\.$NUM\\.$NUM(-$PRE_ID(\\.$PRE_ID)*)?\$"

log() {
	printf '==> %s\n' "$*"
}

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat >&2 <<'EOF'
usage: scripts/release.sh <command> vX.Y.Z[-pre] [-y]

  prepare  set in-repo requirements and version.go to the version
  check    confirm HEAD is the release commit and its tags are free
  tag      check, then tag every published module at HEAD
  push     push the tags to $REMOTE (default origin) in one atomic push
  verify   fetch and build every published module at the version, as a user would
EOF
	exit 2
}

# The published modules' directories: the root and each plugin.
published_dirs() {
	echo .
	git ls-files 'plugin/*/go.mod' | sed 's#/go\.mod$##'
}

# The tag of the module in directory $1.
tag_name() {
	if [ "$1" = . ]; then
		echo "$VERSION"
	else
		echo "$1/$VERSION"
	fi
}

# The module path directory $1 has to declare for the version's major version.
want_path() {
	local path="$REPO_PATH"
	[ "$1" = . ] || path="$path/$1"
	[ "$MAJOR" -lt 2 ] || path="$path/v$MAJOR"
	echo "$path"
}

module_path() {
	awk '$1 == "module" { print $2; exit }' "$1"
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

has_replace() {
	awk -v path="$2" '$1 == "replace" && $2 == path { found = 1 } END { exit !found }' "$1"
}

version_go() {
	sed -nE 's/^[[:space:]]*Version[[:space:]]*=[[:space:]]*"([^"]*)".*/\1/p' version.go
}

problems=()

problem() {
	problems+=("$*")
}

fail_on_problems() {
	if [ ${#problems[@]} -gt 0 ]; then
		printf 'error: %s\n' "${problems[@]}" >&2
		exit 1
	fi
}

check_paths() {
	local dir got want hint=""
	[ "$MAJOR" -lt 2 ] || hint=" (scripts/set-major-version.sh $MAJOR moves them)"
	for dir in $(published_dirs); do
		got="$(module_path "$dir/go.mod")"
		want="$(want_path "$dir")"
		[ "$got" = "$want" ] || problem "$dir/go.mod declares $got, want $want for $VERSION$hint"
	done
}

check_requires() {
	local gomod path ver
	for gomod in $(git ls-files '*go.mod'); do
		while read -r path ver; do
			[ -n "$path" ] || continue
			[ "$ver" = "$VERSION" ] ||
				problem "$gomod requires $path $ver, want $VERSION (scripts/release.sh prepare $VERSION sets it)"
			has_replace "$gomod" "$path" ||
				problem "$gomod requires $path with no replace directive, so builds here would fetch it"
		done <<< "$(repo_requires "$gomod")"
	done
}

check_version_go() {
	local got
	got="$(version_go)"
	[ "$got" = "${VERSION#v}" ] ||
		problem "version.go has Version = \"$got\", want \"${VERSION#v}\" (scripts/release.sh prepare $VERSION sets it)"
}

check_clean() {
	[ -z "$(git status --porcelain --untracked-files=no)" ] ||
		problem "the working tree has uncommitted changes; the release commit has to be HEAD"
}

check_tags_free() {
	local dir tag refs=() taken sha ref
	for dir in $(published_dirs); do
		tag="$(tag_name "$dir")"
		if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
			problem "tag $tag already exists here"
		fi
		refs+=("refs/tags/$tag")
	done
	taken="$(git ls-remote --tags "$REMOTE" "${refs[@]}")" || die "cannot list the tags on $REMOTE"
	while read -r sha ref; do
		[ -z "$ref" ] || problem "tag ${ref#refs/tags/} already exists on $REMOTE"
	done <<< "$taken"
}

cmd_prepare() {
	check_paths
	fail_on_problems

	local gomod path ver args total=0 changed=0
	for gomod in $(git ls-files '*go.mod'); do
		args=()
		while read -r path ver; do
			[ -n "$path" ] || continue
			total=$((total + 1))
			[ "$ver" != "$VERSION" ] || continue
			args+=("-require=$path@$VERSION")
		done <<< "$(repo_requires "$gomod")"
		if [ ${#args[@]} -gt 0 ]; then
			(cd "$(dirname "$gomod")" && go mod edit "${args[@]}")
			changed=$((changed + ${#args[@]}))
		fi
	done

	WANT="${VERSION#v}" perl -0pi -e \
		's/^(\s*Version\s*=\s*)"[^"]*"/$1"$ENV{WANT}"/m or die "version.go: no Version constant\n"' version.go

	log "set $changed of $total in-repo requirements, and Version in version.go, to $VERSION"
	log "next: commit and push this, let CI pass, then run scripts/release.sh tag $VERSION"
}

cmd_check() {
	check_paths
	check_requires
	check_version_go
	check_clean
	check_tags_free
	fail_on_problems
	log "HEAD $(git rev-parse --short HEAD) is ready to be tagged $VERSION ($(published_dirs | wc -l | tr -d ' ') modules)"
}

cmd_tag() {
	cmd_check
	local dir head n=0
	head="$(git rev-parse HEAD)"
	for dir in $(published_dirs); do
		git tag "$(tag_name "$dir")" "$head"
		n=$((n + 1))
	done
	log "tagged $n modules at $(git rev-parse --short HEAD): $VERSION and plugin/<name>/$VERSION"
	log "next: scripts/release.sh push $VERSION"
}

cmd_push() {
	local dir tag sha head="" refs=() taken answer
	for dir in $(published_dirs); do
		tag="$(tag_name "$dir")"
		sha="$(git rev-parse -q --verify "refs/tags/$tag^{commit}")" ||
			die "tag $tag does not exist; scripts/release.sh tag $VERSION creates it"
		[ -z "$head" ] || [ "$sha" = "$head" ] || die "tag $tag is on $sha, not on $head like $VERSION"
		head="$sha"
		refs+=("refs/tags/$tag")
	done

	# A tag on a commit that no branch holds would publish code CI never saw.
	git fetch --quiet "$REMOTE" || die "cannot fetch $REMOTE"
	[ -n "$(git for-each-ref --contains "$head" --format='%(refname)' "refs/remotes/$REMOTE/")" ] ||
		die "$head is on no branch of $REMOTE; push the release commit and let CI pass first"
	taken="$(git ls-remote --tags "$REMOTE" "${refs[@]}")" || die "cannot list the tags on $REMOTE"
	[ -z "$taken" ] || die "some $VERSION tags already exist on $REMOTE:"$'\n'"$taken"

	if [ "$YES" != 1 ]; then
		[ -t 0 ] || die "no terminal to confirm on; pass -y to push anyway"
		read -r -p "push ${#refs[@]} tags for $VERSION at ${head:0:12} to $REMOTE? They cannot be moved once the module proxy has seen them. [y/N] " answer
		case "$answer" in
			y | Y | yes) ;;
			*) die "not pushed" ;;
		esac
	fi

	# One atomic push, so the remote gets every tag or none. GitHub creates no
	# push event for more than three tags at once; a workflow triggered by a
	# release tag would need that tag pushed on its own.
	git push --atomic "$REMOTE" "${refs[@]}"
	log "pushed ${#refs[@]} tags; next: scripts/release.sh verify $VERSION"
}

cmd_verify() {
	local work dir path out n=0 failed=0
	work="$(mktemp -d "${TMPDIR:-/tmp}/pinpoint-release.XXXXXX")"
	trap "rm -rf '$work'" EXIT
	# A fresh module per published module, with no replace directives and no
	# workspace: what a user's go get sees.
	export GOFLAGS=-mod=mod GOWORK=off
	log "fetching through GOPROXY=$(go env GOPROXY)"
	for dir in $(published_dirs); do
		path="$(want_path "$dir")"
		mkdir "$work/$n"
		if out="$(cd "$work/$n" && go mod init example.com/release-verify 2>&1 &&
			go get "$path@$VERSION" 2>&1 && go build -o /dev/null "$path" 2>&1)"; then
			echo "ok    $path@$VERSION"
		else
			echo "FAIL  $path@$VERSION"
			printf '%s\n' "$out" | tail -n 3 | sed 's/^/      /'
			failed=$((failed + 1))
		fi
		n=$((n + 1))
	done
	[ "$failed" -eq 0 ] || die "$failed of $n modules did not resolve and build at $VERSION"
	log "all $n modules resolve and build at $VERSION from outside the repository"
}

[ $# -ge 2 ] || usage
COMMAND="$1"
[[ "$2" =~ $SEMVER ]] || die "not a module version: $2 (want vX.Y.Z or vX.Y.Z-pre, such as v2.0.0-rc.1)"
VERSION="$2"
MAJOR="${BASH_REMATCH[1]}"
shift 2
YES=0
while [ $# -gt 0 ]; do
	case "$1" in
		-y | --yes) YES=1 ;;
		*) usage ;;
	esac
	shift
done

cd "$ROOT_DIR"
case "$COMMAND" in
	prepare) cmd_prepare ;;
	check) cmd_check ;;
	tag) cmd_tag ;;
	push) cmd_push ;;
	verify) cmd_verify ;;
	*) usage ;;
esac
