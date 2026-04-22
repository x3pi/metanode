# Stage 1: Build Environment
FROM golang:1.23-bookworm AS builder

# Install required build dependencies
RUN apt-get update && apt-get install -y \
    build-essential \
    cmake \
    git \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Install Rust toolchain for NOMT FFI
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
ENV PATH="/root/.cargo/bin:${PATH}"

WORKDIR /app

# Download Go modules first to cache them
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# 1. Build C++ EVM Linker
RUN echo "Building C++ EVM Linker..." && \
    cd pkg/mvm/linker && \
    mkdir -p build && cd build && \
    cmake .. && make -j$(nproc)

# 2. Build Rust NOMT FFI (libmtn_nomt.a)
RUN echo "Building Rust NOMT FFI..." && \
    cd pkg/nomt_ffi/rust_lib && \
    cargo build --release

# 3. Build Go Binary
RUN echo "Building Go application..." && \
    cd cmd/simple_chain && \
    go build -o /app/simple_chain .

# Stage 2: Minimal Runtime Image
FROM debian:bookworm-slim

# Install runtime dependencies (C standards, ca-certs for HTTPS)
RUN apt-get update && apt-get install -y \
    ca-certificates \
    libc6 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Create non-root user
RUN groupadd -r metanode && useradd -r -g metanode metanode

# Copy the compiled binary from builder
COPY --from=builder --chown=metanode:metanode /app/simple_chain /app/simple_chain

# Expose default RPC port
EXPOSE 8545 9001 8700

USER metanode

# Define data volume (should be mounted as btrfs/xfs for snapshots)
VOLUME ["/app/data"]

# Entrypoint expects config file path as argument: -config=/path/to/config.json
ENTRYPOINT ["/app/simple_chain"]
