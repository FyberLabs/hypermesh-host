# hypermesh-test — GGUF + OpenAI-shaped /v1/chat/completions on :8080
#
# JP7 / L4T R39 host — Ubuntu 24.04 + CUDA 13 SBSA (aarch64)
# Pin a digest in CI after a good on-box build. Do not invent repo@digest here.
# Do not use JP6 / ubuntu:22.04 / r36.* / l4t-jetpack (gone in 7.x).
# Do not invent l4t-jetpack:r39.2.1. No whitesscott/l4t-jetpack unless
# Product explicitly asks.
#
# Hardware Research / Product lock default (compile llama.cpp in-image):
#   nvcr.io/nvidia/cuda:13.2.1-devel-ubuntu24.04
# Runtime alternative (copy a prebuilt binary only; not this recipe):
#   nvcr.io/nvidia/cuda:13.2.1-runtime-ubuntu24.04
# Matches JetPack 7.2 host CUDA 13.2.1. Tag was registry-listed
# (linux/amd64 + linux/arm64). Not an on-box pull; unverified-until-AGX-build.
#
# Build this ON AGX64-1 after the JetPack 7.2.1 / L4T R39.2.1 overlay:
#   sudo docker build --platform linux/arm64 \
#     -t ghcr.io/fyberlabs/hypermesh-test .
# Docker 29 Buildx does not accept --runtime=nvidia at build. CDI
# --allow device + RUN --device= also failed the llama-server link
# (libcuda.so.1 / CUDA symbols undefined). Link against CUDA devel
# stubs (see cmake below). Build does not need weights. Do not COPY
# model.gguf (eMMC cannot bake 4.9G).
# Runtime: docker run --runtime=nvidia \
#   -v /home/chris/hypermesh/workload/model.gguf:/models/model.gguf:ro ...
# (not x86 --gpus). Host driver supplies the real libcuda.so.1.
# Arch: sm_87, cmake -DCMAKE_CUDA_ARCHITECTURES=87
#
# Fallback only if 13.2.1 driver-allowlist rejects on-box:
#   FROM nvcr.io/nvidia/cuda:13.0.0-devel-ubuntu24.04
#   and docker build/run -e NVIDIA_DISABLE_REQUIRE=true
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
    && rm -rf /var/lib/apt/lists/*

# Toolkit is in the NGC CUDA devel image. Host driver comes from the AGX
# at run via --runtime=nvidia (not x86 --gpus). Build links stubs only.
ARG CUDA_HOME=/usr/local/cuda
ENV PATH="${CUDA_HOME}/bin:${PATH}"
ENV LD_LIBRARY_PATH="${CUDA_HOME}/lib64:/usr/local/lib:${LD_LIBRARY_PATH:-}"

# AGX Orin is SM 8.7. Override LLAMA_CPP_REF if you need a different llama.cpp tag.
# Pin v0.5.0 (latest stable ggml-org/llama.cpp release, 2026-09-23; tag
# commit 7fe450e19305b828c199d602c23a8337aaa1f03b). Previous pin b4689
# (90e4dba, 2025-02-11) predates streamed tool calls: llama-server returned
# HTTP 500 "Cannot use tools with stream" and empty tool_call ids, so
# editor clients (Cursor / Cline / Continue / Copilot stream with tools)
# could not use a lease. Streamed tool calls + generated tool_call ids
# landed in ggml-org/llama.cpp#12379 (merge f5cd27b, 2025-05-25).
# In v0.5.0 --jinja is ON by default (LLAMA_ARG_JINJA env still honoured).
ARG LLAMA_CPP_REF=v0.5.0
WORKDIR /src
# llama.cpp v0.5.0 defaults BUILD_SHARED_LIBS=ON. CMAKE_LIBRARY_OUTPUT_DIRECTORY
# is build/bin (same dir as llama-server). cmake --install of the whole
# tree also install()s every tool RUNTIME that was not built
# (--target llama-server only), so copy the shared libs cmake actually wrote.
# AGX64-1 live image that only installed the binary then rm -rf /src had
# empty /usr/local/lib and Exit 127 (libllama.so not found) — keep the copy.
# v0.5.0 vs b4689 build changes:
#   - llama-server now also links shared libllama-server-impl.so,
#     libllama-common.so and libmtmd.so (b4689 had common STATIC and no
#     mtmd), so copy build/bin/lib*.so* instead of the old
#     libllama.so* libggml*.so* list (that list would Exit 127 again).
#     The ldd guard below fails the build if anything but libcuda is missing.
#   - GGML_CUDA is the CUDA switch (LLAMA_CUBLAS is a FATAL_ERROR; we
#     already used GGML_CUDA). CMAKE_CUDA_ARCHITECTURES=87 unchanged.
#   - LLAMA_CURL is deprecated/ignored; HTTPS is LLAMA_OPENSSL. The jailed
#     workload has no egress and never downloads models, so OPENSSL=OFF and
#     libcurl4-openssl-dev is dropped.
#   - Web UI assets: LLAMA_USE_PREBUILT_UI=ON would fetch from a HF bucket
#     at build time. OFF keeps the build offline/deterministic; the API
#     (/v1/chat/completions) is unaffected.
# Expected /usr/local/lib set (v0.5.0, GGML_CUDA=ON, GGML_BACKEND_DL=OFF):
#   libllama.so libllama-common.so libllama-server-impl.so libmtmd.so
#   libggml.so libggml-base.so libggml-cpu.so libggml-cuda.so (+ SONAME links)
# CUDA devel stubs (NVIDIA standard link path): Docker 29 Buildx cannot
# inject the host driver at build. The devel image ships
# ${CUDA_HOME}/lib64/stubs/libcuda.so (SONAME libcuda.so.1). Symlink the
# versioned name and pass -L / -Wl,-rpath-link so llama-server and
# libggml-cuda.so finish the link. Do not add stubs to LD_LIBRARY_PATH
# — runtime --runtime=nvidia provides the real driver.
RUN ln -sf "${CUDA_HOME}/lib64/stubs/libcuda.so" "${CUDA_HOME}/lib64/stubs/libcuda.so.1" \
    && git clone --depth 1 --branch "${LLAMA_CPP_REF}" https://github.com/ggml-org/llama.cpp.git \
    && cmake -S llama.cpp -B build \
        -DGGML_CUDA=ON \
        -DCMAKE_CUDA_ARCHITECTURES=87 \
        -DLLAMA_BUILD_IS_DEV=OFF \
        -DLLAMA_OPENSSL=OFF \
        -DLLAMA_USE_PREBUILT_UI=OFF \
        -DLLAMA_BUILD_TESTS=OFF \
        -DCMAKE_EXE_LINKER_FLAGS="-L${CUDA_HOME}/lib64/stubs -Wl,-rpath-link,${CUDA_HOME}/lib64/stubs" \
        -DCMAKE_SHARED_LINKER_FLAGS="-L${CUDA_HOME}/lib64/stubs -Wl,-rpath-link,${CUDA_HOME}/lib64/stubs" \
    && cmake --build build -j"$(nproc)" --target llama-server \
    && install -m 0755 build/bin/llama-server /usr/local/bin/llama-server \
    && mkdir -p /usr/local/lib \
    && cp -a build/bin/lib*.so* /usr/local/lib/ \
    && ldconfig \
    && ! (ldd /usr/local/bin/llama-server | grep -v 'libcuda.so' | grep 'not found') \
    && rm -rf /src

# Weights stay on the host. Catalog id stays llama-3.1-8b-q4.
# Do not COPY model.gguf. Entrypoint fails if the run-time mount is missing.
RUN mkdir -p /models
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh

EXPOSE 8080
# 0.0.0.0 so docker -p 8080:8080 and the WG client_address can reach it.
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
