# hypermesh-test — GGUF + OpenAI-shaped /v1/chat/completions on :8080
#
# JP7 / L4T R39 host — Ubuntu 24.04 + CUDA 13 SBSA (aarch64)
# Pin a digest in CI after a good on-box build. Do not invent repo@digest here.
# Do not use JP6 / ubuntu:22.04 / r36.* / l4t-jetpack (gone in 7.x).
# Do not invent l4t-jetpack:r39.2.1. No whitesscott/l4t-jetpack unless
# Product explicitly asks.
#
# Hardware Research default (compile llama.cpp in-image): devel, not runtime.
# Matches JetPack 7.2 host CUDA 13.2.1. Tag was registry-listed
# (linux/amd64 + linux/arm64). Not an on-box pull; unverified-until-AGX-build.
#
# Build this ON AGX64-1 after the JetPack 7.2.1 / L4T R39.2.1 overlay:
#   sudo docker build --runtime=nvidia --platform linux/arm64 \
#     -t ghcr.io/fyberlabs/hypermesh-test .
# Runtime: docker run --runtime=nvidia ...
# Arch: sm_87, cmake -DCMAKE_CUDA_ARCHITECTURES=87
#
# Fallback if 13.2.1 driver-allowlist rejects on-box (r590/r595 gap):
#   nvcr.io/nvidia/cuda:13.0.0-devel-ubuntu24.04
#   or 13.3.x-*-ubuntu24.04 with -e NVIDIA_DISABLE_REQUIRE=true
# That is an on-box rescue, not a second default FROM.
#
# Plain cuda:*-ubuntu24.04 does not ship Jetson cuDNN/TensorRT.
# llama.cpp CUDA decode usually does not need TRT. Only add packages
# from repo.download.nvidia.com/jetson r39.2 if a workload needs them.

FROM nvcr.io/nvidia/cuda:13.2.1-devel-ubuntu24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential \
        cmake \
        git \
        ca-certificates \
        libcurl4-openssl-dev \
    && rm -rf /var/lib/apt/lists/*

# Toolkit is in the NGC CUDA devel image. Host driver still comes from
# the AGX via --runtime=nvidia at build and at run (not x86 --gpus).
ARG CUDA_HOME=/usr/local/cuda
ENV PATH="${CUDA_HOME}/bin:${PATH}"
ENV LD_LIBRARY_PATH="${CUDA_HOME}/lib64:${LD_LIBRARY_PATH:-}"

# AGX Orin is SM 8.7. Override LLAMA_CPP_REF if you need a different llama.cpp tag.
ARG LLAMA_CPP_REF=b4690
WORKDIR /src
RUN git clone --depth 1 --branch "${LLAMA_CPP_REF}" https://github.com/ggerganov/llama.cpp.git \
    && cmake -S llama.cpp -B build \
        -DGGML_CUDA=ON \
        -DCMAKE_CUDA_ARCHITECTURES=87 \
    && cmake --build build -j"$(nproc)" --target llama-server \
    && install -m 0755 build/bin/llama-server /usr/local/bin/llama-server \
    && rm -rf /src

# Weights are not in git. Copy llama-3.1-8b-q4 onto this box as model.gguf, then rebuild.
# Missing file fails the build honestly. Catalog id stays llama-3.1-8b-q4.
COPY model.gguf /models/model.gguf
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh

EXPOSE 8080
# 0.0.0.0 so docker -p 8080:8080 and the WG client_address can reach it.
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
