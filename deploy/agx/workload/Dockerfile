# hypermesh-test — GGUF + OpenAI-shaped /v1/chat/completions on :8080
#
# Build this ON AGX64-1 after the JetPack 7.2.1 / L4T R39.2.1 overlay:
#   sudo docker build --runtime=nvidia -t ghcr.io/fyberlabs/hypermesh-test .
#
# Do not build this on x86. The NGC CUDA tag is multi-arch; an x86 build
# is the wrong image even if the tag name matches.
# After a good on-box build, pin the sha256. The host program does not
# invent image_hash or a repo@digest.
#
# This box stays on JP7 / Ubuntu 24.04 / CUDA 13. Do not keep a JP6 FROM
# as the AGX default. Do not dual-pin a JP6 image next to this one.
# If a leftover JP6 image will not run here: stop.

# Honest JP7 path: NVIDIA NGC CUDA 13 SBSA / aarch64 (Ubuntu 24.04).
# NVIDIA does not publish l4t-jetpack for L4T r39 / JetPack 7.x
# (NGC l4t-jetpack latest remains r36.4.0). JP7 user guides use
# nvcr.io/nvidia/cuda:13.0.0-devel-ubuntu24.04 as the container example.
# This box's host stack is JetPack 7.2.1 / CUDA 13; 13.0.0-devel is the
# documented NGC candidate. Tag was registry-listed (linux/amd64 +
# linux/arm64) when this recipe was drafted. It is not an on-box pull
# and is unverified-until-AGX-build. Do not invent a digest. Pin
# ghcr.io/fyberlabs/hypermesh-test sha256 only after a good AGX build.
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
