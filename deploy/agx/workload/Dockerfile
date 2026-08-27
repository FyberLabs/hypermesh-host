# hypermesh-test — GGUF + OpenAI-shaped /v1/chat/completions on :8080
#
# Build this ON the Jetson AGX Orin after the JetPack 6.2.3 / L4T 36.5.2 overlay:
#   sudo docker build --runtime=nvidia -t ghcr.io/fyberlabs/hypermesh-test .
#
# Do not build this on x86 and pretend it is a Jetson image.
# After a good on-box build, pin the sha256. The host program does not invent a digest.
#
# If this box is on JetPack 7.x and this JP6 image will not run: stop.
# Do not silently run a JP7 image next to this one.

FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential \
        cmake \
        git \
        ca-certificates \
        libcurl4-openssl-dev \
    && rm -rf /var/lib/apt/lists/*

# CUDA toolkit comes from the AGX host via --runtime=nvidia at build time.
ARG CUDA_HOME=/usr/local/cuda
ENV PATH="${CUDA_HOME}/bin:${PATH}"
ENV LD_LIBRARY_PATH="${CUDA_HOME}/lib64:${LD_LIBRARY_PATH:-}"

# Orin is SM 8.7. Override LLAMA_CPP_REF if you need a different llama.cpp tag.
ARG LLAMA_CPP_REF=b4690
WORKDIR /src
RUN git clone --depth 1 --branch "${LLAMA_CPP_REF}" https://github.com/ggerganov/llama.cpp.git \
    && cmake -S llama.cpp -B build \
        -DGGML_CUDA=ON \
        -DCMAKE_CUDA_ARCHITECTURES=87 \
    && cmake --build build -j"$(nproc)" --target llama-server \
    && install -m 0755 build/bin/llama-server /usr/local/bin/llama-server \
    && rm -rf /src

# Weights are not in git. Copy model.gguf onto this box, then rebuild.
# Missing file fails the build honestly.
COPY model.gguf /models/model.gguf
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh

EXPOSE 8080
# 0.0.0.0 so docker -p 8080:8080 and the WG client_address can reach it.
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
