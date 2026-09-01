#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTO_SRC_DIR="${PROTO_SRC_DIR:-$ROOT_DIR/pinpoint-grpc-idl/proto}"
OUT_DIR="${OUT_DIR:-$ROOT_DIR/protobuf}"
MOCK_OUT_DIR="${MOCK_OUT_DIR:-$ROOT_DIR/protobuf/mock}"
TOOLS_DIR="${TOOLS_DIR:-$ROOT_DIR/.tools}"
BIN_DIR="$TOOLS_DIR/bin"

PROTOC_VERSION="${PROTOC_VERSION:-36.1}"
PROTOC_GEN_GO_VERSION="${PROTOC_GEN_GO_VERSION:-v1.36.11}"
PROTOC_GEN_GO_GRPC_VERSION="${PROTOC_GEN_GO_GRPC_VERSION:-v1.6.2}"
PROTOC_GEN_GO_GRPCMOCK_VERSION="${PROTOC_GEN_GO_GRPCMOCK_VERSION:-v1.3.2}"
MOCK_GO_PACKAGE="${MOCK_GO_PACKAGE:-github.com/pinpoint-apm/pinpoint-go-agent/protobuf;grpcmock}"
# Log.proto describes a log-shipping service this agent does not implement;
# generating it would ship a client and a mock nothing ever calls.
EXCLUDED_PROTOS="${EXCLUDED_PROTOS:-Log.proto}"
TMP_PROTO_DIR=""

log() {
	printf '==> %s\n' "$*"
}

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

cleanup() {
	if [ -n "${TMP_PROTO_DIR:-}" ]; then
		rm -rf "$TMP_PROTO_DIR"
	fi
}

have() {
	command -v "$1" >/dev/null 2>&1
}

download() {
	local url="$1"
	local dest="$2"

	if have curl; then
		curl -fL "$url" -o "$dest"
	elif have wget; then
		wget -O "$dest" "$url"
	else
		die "curl or wget is required to download protoc"
	fi
}

protoc_platform() {
	local os arch

	case "$(uname -s)" in
		Darwin)
			os="osx"
			;;
		Linux)
			os="linux"
			;;
		*)
			die "unsupported OS: $(uname -s)"
			;;
	esac

	case "$(uname -m)" in
		x86_64|amd64)
			arch="x86_64"
			;;
		arm64|aarch64)
			arch="aarch_64"
			;;
		*)
			die "unsupported architecture: $(uname -m)"
			;;
	esac

	printf '%s-%s' "$os" "$arch"
}

ensure_protoc() {
	if have protoc; then
		log "using protoc: $(command -v protoc)"
		return
	fi

	mkdir -p "$BIN_DIR"

	local platform zip_name url install_dir zip_path
	platform="$(protoc_platform)"
	zip_name="protoc-${PROTOC_VERSION}-${platform}.zip"
	url="https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/${zip_name}"
	install_dir="$TOOLS_DIR/protoc-${PROTOC_VERSION}-${platform}"
	zip_path="$TOOLS_DIR/${zip_name}"

	if [ ! -x "$install_dir/bin/protoc" ]; then
		log "downloading protoc ${PROTOC_VERSION} for ${platform}"
		mkdir -p "$install_dir"
		have unzip || die "unzip is required to unpack protoc"
		download "$url" "$zip_path"
		unzip -q "$zip_path" -d "$install_dir"
	fi

	ln -sf "$install_dir/bin/protoc" "$BIN_DIR/protoc"
	log "using protoc: $BIN_DIR/protoc"
}

ensure_go_tool() {
	local bin_name="$1"
	local module="$2"
	local version="$3"

	if have "$bin_name"; then
		log "using $bin_name: $(command -v "$bin_name")"
		return
	fi

	have go || die "go is required to install $bin_name"

	log "installing $bin_name $version"
	mkdir -p "$BIN_DIR"
	GOBIN="$BIN_DIR" go install "${module}@${version}"
}

# collect_proto_sources fills PROTO_FILES with the .proto files to generate,
# minus EXCLUDED_PROTOS.
collect_proto_sources() {
	[ -d "$PROTO_SRC_DIR/v1" ] || die "missing proto source directory: $PROTO_SRC_DIR/v1"

	local f base
	PROTO_FILES=()
	for f in "$PROTO_SRC_DIR"/v1/*.proto; do
		[ -e "$f" ] || continue
		base="$(basename "$f")"
		case " $EXCLUDED_PROTOS " in
			*" $base "*)
				log "skipping $base"
				continue
				;;
		esac
		PROTO_FILES+=("$f")
	done

	[ ${#PROTO_FILES[@]} -gt 0 ] || die "no proto files found in $PROTO_SRC_DIR/v1"
}

generate() {
	mkdir -p "$OUT_DIR"

	local generated_dir
	TMP_PROTO_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pinpoint-proto.XXXXXX")"
	generated_dir="$TMP_PROTO_DIR/generated"
	mkdir -p "$generated_dir"

	log "generating Go protobuf files from pinpoint-grpc-idl"
	protoc \
		--proto_path="$PROTO_SRC_DIR" \
		--go_out="$generated_dir" \
		--go_opt=paths=source_relative \
		--go-grpc_out="$generated_dir" \
		--go-grpc_opt=paths=source_relative \
		"${PROTO_FILES[@]}"

	[ -d "$generated_dir/v1" ] || die "generated v1 directory was not created"

	find "$OUT_DIR" -maxdepth 1 -type f -name '*.pb.go' -delete
	cp "$generated_dir"/v1/*.pb.go "$OUT_DIR"/
	gofmt -w "$OUT_DIR"/*.pb.go
}

# generate_mocks emits testify mocks for every generated gRPC client, server and
# stream into protobuf/mock, a package of its own so that testify stays out of
# the protobuf package the agent ships. The M options below name the protobuf
# package as the one to import and "grpcmock" as the package to emit, which is
# what import_package=true keys off; the package is named grpcmock rather than
# mock so that importers can still call the testify package mock.
generate_mocks() {
	local mock_opts generated_dir f

	mock_opts=()
	for f in "${PROTO_FILES[@]}"; do
		mock_opts+=(--go-grpcmock_opt="Mv1/$(basename "$f")=$MOCK_GO_PACKAGE")
	done

	generated_dir="$TMP_PROTO_DIR/generated-mocks"
	mkdir -p "$generated_dir" "$MOCK_OUT_DIR"

	log "generating testify gRPC mocks"
	protoc \
		--proto_path="$PROTO_SRC_DIR" \
		--go-grpcmock_out="$generated_dir" \
		--go-grpcmock_opt=paths=source_relative \
		--go-grpcmock_opt=framework=testify \
		--go-grpcmock_opt=import_package=true \
		"${mock_opts[@]}" \
		"${PROTO_FILES[@]}"

	[ -d "$generated_dir/v1" ] || die "generated mock directory was not created"

	# protoc-gen-go-grpcmock v1.3.2 qualifies a stream interface with the
	# package of the method's response message instead of the service's own.
	# Every client-streaming RPC here answers google.protobuf.Empty, so those
	# come out as emptypb.Span_SendSpanClient and friends, which do not exist.
	# (\b is GNU-only, so key off the underscore every stream type carries;
	# emptypb.Empty, the one legitimate use of that import, has none.)
	sed -i.bak -E 's/emptypb\.([A-Za-z0-9]+_[A-Za-z0-9_]+)/protobuf.\1/g' \
		"$generated_dir"/v1/*_grpc_mock.pb.go
	rm -f "$generated_dir"/v1/*.bak

	find "$MOCK_OUT_DIR" -maxdepth 1 -type f -name '*_grpc_mock.pb.go' -delete
	cp "$generated_dir"/v1/*_grpc_mock.pb.go "$MOCK_OUT_DIR"/
	gofmt -w "$MOCK_OUT_DIR"/*.pb.go
}

export PATH="$BIN_DIR:$PATH"
trap cleanup EXIT

ensure_protoc
ensure_go_tool protoc-gen-go google.golang.org/protobuf/cmd/protoc-gen-go "$PROTOC_GEN_GO_VERSION"
ensure_go_tool protoc-gen-go-grpc google.golang.org/grpc/cmd/protoc-gen-go-grpc "$PROTOC_GEN_GO_GRPC_VERSION"
ensure_go_tool protoc-gen-go-grpcmock github.com/lovoo/protoc-gen-go-grpcmock/cmd/protoc-gen-go-grpcmock "$PROTOC_GEN_GO_GRPCMOCK_VERSION"
collect_proto_sources
generate
generate_mocks
log "protobuf generation complete"
