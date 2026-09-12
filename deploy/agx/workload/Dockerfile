# hypermesh-test — GGUF + OpenAI-shaped /v1/chat/completions on :8080
#
# JP7 / L4T R39 host — Ubuntu 24.04 + CUDA 13 SBSA (aarch64)
# Pin a digest in CI after a good on-box build. Do not invent repo@digest here.
# Do not use JP6 / ubuntu:22.04 / l4t-jetpack (gone in 7.x).
#
# Build this ON AGX64-1 after the JetPack 7.2.1 / L4T R39.2.1 overlay:
#   sudo docker build --runtime=nvidia --platform linux/arm64 \
#     -t ghcr.io/fyberlabs/hypermesh-test .
# Runtime: docker run --runtime=nvidia ...
#
# Do not build this on x86. The NGC CUDA tag is multi-arch; an x86 build
# is the wrong image even if the tag name matches. Verify on the AGX with
# --platform linux/arm64.
#
# CUDA in the image need not equal the host toolkit minor. L4T major MUST
# match the host (r39). Host toolkit is CUDA 13.2; 13.2.1-devel-ubuntu24.04
# is registry-listed. This recipe keeps the NVIDIA JP 7.2 / Orin guide
# example 13.0.0-devel-ubuntu24.04. Tag listing is not an on-box pull and
# is unverified-until-AGX-build.

FROM nvcr.io/nvidia/cuda:13.0.0-devel-ubuntu24.04

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
