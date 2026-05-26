#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTO_SRC_DIR="${PROTO_SRC_DIR:-$ROOT_DIR/pinpoint-grpc-idl/proto}"
OUT_DIR="${OUT_DIR:-$ROOT_DIR/protobuf}"
TOOLS_DIR="${TOOLS_DIR:-$ROOT_DIR/.tools}"
BIN_DIR="$TOOLS_DIR/bin"

PROTOC_VERSION="${PROTOC_VERSION:-28.3}"
PROTOC_GEN_GO_VERSION="${PROTOC_GEN_GO_VERSION:-v1.35.1}"
PROTOC_GEN_GO_GRPC_VERSION="${PROTOC_GEN_GO_GRPC_VERSION:-v1.5.1}"
GO_PACKAGE="${GO_PACKAGE:-github.com/pinpoint-apm/pinpoint-go-agent/protobuf;v1}"
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

generate() {
	[ -d "$PROTO_SRC_DIR/v1" ] || die "missing proto source directory: $PROTO_SRC_DIR/v1"
	mkdir -p "$OUT_DIR"

	local proto_files
	proto_files=("$PROTO_SRC_DIR"/v1/*.proto)
	[ -e "${proto_files[0]}" ] || die "no proto files found in $PROTO_SRC_DIR/v1"

	local generated_dir
	TMP_PROTO_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pinpoint-proto.XXXXXX")"
	generated_dir="$TMP_PROTO_DIR/generated"
	mkdir -p "$generated_dir"

	log "generating Go protobuf files from pinpoint-grpc-idl"
	protoc \
		--proto_path="$PROTO_SRC_DIR" \
		--go_out="$generated_dir" \
		--go_opt=paths=source_relative \
		--go_opt="Mv1/Log.proto=$GO_PACKAGE" \
		--go-grpc_out="$generated_dir" \
		--go-grpc_opt=paths=source_relative \
		--go-grpc_opt="Mv1/Log.proto=$GO_PACKAGE" \
		"${proto_files[@]}"

	[ -d "$generated_dir/v1" ] || die "generated v1 directory was not created"

	find "$OUT_DIR" -maxdepth 1 -type f -name '*.pb.go' -delete
	cp "$generated_dir"/v1/*.pb.go "$OUT_DIR"/
	gofmt -w "$OUT_DIR"/*.pb.go
}

export PATH="$BIN_DIR:$PATH"
trap cleanup EXIT

ensure_protoc
ensure_go_tool protoc-gen-go google.golang.org/protobuf/cmd/protoc-gen-go "$PROTOC_GEN_GO_VERSION"
ensure_go_tool protoc-gen-go-grpc google.golang.org/grpc/cmd/protoc-gen-go-grpc "$PROTOC_GEN_GO_GRPC_VERSION"
generate
log "protobuf generation complete"
