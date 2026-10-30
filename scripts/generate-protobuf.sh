#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTO_SRC_DIR="$ROOT_DIR/pinpoint-grpc-idl/proto"
OUT_DIR="$ROOT_DIR/internal/protobuf"
MOCK_OUT_DIR="$ROOT_DIR/internal/protobuf/mock"
TEST_PROTO_DIR="$ROOT_DIR/test/testapp"
TOOLS_DIR="$ROOT_DIR/.tools"
BIN_DIR="$TOOLS_DIR/bin"

PROTOC_VERSION="36.1"
PROTOC_GEN_GO_VERSION="v1.36.11"
PROTOC_GEN_GO_GRPC_VERSION="v1.6.2"
PROTOC_GEN_GO_GRPCMOCK_VERSION="v1.3.2"
MOCK_GO_PACKAGE="github.com/pinpoint-apm/pinpoint-go-agent/v2/internal/protobuf;grpcmock"

log() {
	printf '==> %s\n' "$*"
}

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

TMP="$(mktemp -d "${TMPDIR:-/tmp}/pinpoint-proto.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

# ensure_protoc puts the pinned protoc release in BIN_DIR, downloading it once.
ensure_protoc() {
	local os arch
	case "$(uname -s)" in
		Darwin) os=osx ;;
		Linux) os=linux ;;
		*) die "unsupported OS: $(uname -s)" ;;
	esac
	case "$(uname -m)" in
		x86_64 | amd64) arch=x86_64 ;;
		arm64 | aarch64) arch=aarch_64 ;;
		*) die "unsupported architecture: $(uname -m)" ;;
	esac

	local name="protoc-${PROTOC_VERSION}-${os}-${arch}"
	local dir="$TOOLS_DIR/$name"
	if [ ! -x "$dir/bin/protoc" ]; then
		log "downloading protoc ${PROTOC_VERSION} for ${os}-${arch}"
		mkdir -p "$dir"
		curl -fsSL -o "$TMP/protoc.zip" \
			"https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/${name}.zip"
		unzip -q "$TMP/protoc.zip" -d "$dir"
	fi
	mkdir -p "$BIN_DIR"
	ln -sf "$dir/bin/protoc" "$BIN_DIR/protoc"
}

# install_go_tool installs the pinned plugin into BIN_DIR, which comes first on
# PATH: every plugin stamps its own version into the files it writes, so a copy
# found elsewhere on PATH would silently rewrite the whole generated tree. The
# exact @version is the pin, and go install leaves an up-to-date binary alone.
# The optional third argument is passed as -ldflags: protoc-gen-go-grpcmock
# keeps its version in a variable whose default is "dev".
install_go_tool() {
	local module="$1" version="$2" ldflags="${3:-}"
	log "installing ${module##*/} $version"
	GOBIN="$BIN_DIR" go install ${ldflags:+-ldflags "$ldflags"} "${module}@${version}"
}

generate() {
	log "generating Go protobuf files from pinpoint-grpc-idl"
	mkdir -p "$TMP/generated" "$OUT_DIR"
	protoc \
		--proto_path="$PROTO_SRC_DIR" \
		--go_out="$TMP/generated" \
		--go_opt=paths=source_relative \
		--go-grpc_out="$TMP/generated" \
		--go-grpc_opt=paths=source_relative \
		"${PROTO_FILES[@]}"

	find "$OUT_DIR" -maxdepth 1 -type f -name '*.pb.go' -delete
	cp "$TMP"/generated/v1/*.pb.go "$OUT_DIR"/
	gofmt -w "$OUT_DIR"/*.pb.go
}

# generate_mocks emits testify mocks for every generated gRPC client, server and
# stream into internal/protobuf/mock, a package of its own so that testify stays
# out of the protobuf package the agent ships. The testify version each header
# records is the one the generator itself was built against, read out of its
# build info - not the one this module requires, which only has to be new enough
# to compile what the generator wrote. The M options below name the protobuf
# package as the one to import and "grpcmock" as the package to emit, which is
# what import_package=true keys off; the package is named grpcmock rather than
# mock so that importers can still call the testify package mock.
generate_mocks() {
	local f mock_opts=()
	for f in "${PROTO_FILES[@]}"; do
		mock_opts+=(--go-grpcmock_opt="Mv1/$(basename "$f")=$MOCK_GO_PACKAGE")
	done

	log "generating testify gRPC mocks"
	mkdir -p "$TMP/mocks" "$MOCK_OUT_DIR"
	protoc \
		--proto_path="$PROTO_SRC_DIR" \
		--go-grpcmock_out="$TMP/mocks" \
		--go-grpcmock_opt=paths=source_relative \
		--go-grpcmock_opt=framework=testify \
		--go-grpcmock_opt=import_package=true \
		"${mock_opts[@]}" \
		"${PROTO_FILES[@]}"

	# protoc-gen-go-grpcmock v1.3.2 qualifies a stream interface with the
	# package of the method's response message instead of the service's own.
	# Every client-streaming RPC here answers google.protobuf.Empty, so those
	# come out as emptypb.Span_SendSpanClient and friends, which do not exist.
	# (\b is GNU-only, so key off the underscore every stream type carries;
	# emptypb.Empty, the one legitimate use of that import, has none.)
	sed -i.bak -E 's/emptypb\.([A-Za-z0-9]+_[A-Za-z0-9_]+)/protobuf.\1/g' \
		"$TMP"/mocks/v1/*_grpc_mock.pb.go
	rm -f "$TMP"/mocks/v1/*.bak

	find "$MOCK_OUT_DIR" -maxdepth 1 -type f -name '*_grpc_mock.pb.go' -delete
	cp "$TMP"/mocks/v1/*_grpc_mock.pb.go "$MOCK_OUT_DIR"/
	gofmt -w "$MOCK_OUT_DIR"/*.pb.go
}

# generate_testapp compiles test/testapp/testapp.proto, the gRPC service the
# end-to-end suite and the grpc examples talk over, in place: its go_package is
# that directory, and it is a test fixture, not part of the collector protocol.
generate_testapp() {
	log "generating the testapp test service"
	find "$TEST_PROTO_DIR" -maxdepth 1 -type f -name '*.pb.go' -delete
	protoc \
		--proto_path="$TEST_PROTO_DIR" \
		--go_out="$TEST_PROTO_DIR" \
		--go_opt=paths=source_relative \
		--go-grpc_out="$TEST_PROTO_DIR" \
		--go-grpc_opt=paths=source_relative \
		"$TEST_PROTO_DIR/testapp.proto"
	gofmt -w "$TEST_PROTO_DIR"/*.pb.go
}

export PATH="$BIN_DIR:$PATH"

ensure_protoc
install_go_tool google.golang.org/protobuf/cmd/protoc-gen-go "$PROTOC_GEN_GO_VERSION"
install_go_tool google.golang.org/grpc/cmd/protoc-gen-go-grpc "$PROTOC_GEN_GO_GRPC_VERSION"
install_go_tool github.com/lovoo/protoc-gen-go-grpcmock/cmd/protoc-gen-go-grpcmock \
	"$PROTOC_GEN_GO_GRPCMOCK_VERSION" "-X main.version=$PROTOC_GEN_GO_GRPCMOCK_VERSION"

# Log.proto describes a log-shipping service this agent does not implement;
# generating it would ship a client and a mock nothing ever calls.
PROTO_FILES=()
for f in "$PROTO_SRC_DIR"/v1/*.proto; do
	[ "$(basename "$f")" = Log.proto ] || PROTO_FILES+=("$f")
done
[ ${#PROTO_FILES[@]} -gt 0 ] || die "no proto files found in $PROTO_SRC_DIR/v1 (is the pinpoint-grpc-idl submodule checked out?)"

generate
generate_mocks
generate_testapp
log "protobuf generation complete"
