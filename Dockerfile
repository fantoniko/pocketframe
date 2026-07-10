FROM ubuntu:20.04@sha256:8feb4d8ca5354def3d8fce243717141ce31e2c428701f6682bd2fafe15388214 AS builder

# Install required packages, gcc-multilib for the 32bit ELF support
RUN apt-get update && apt-get -y install --no-install-recommends build-essential ca-certificates git gcc-multilib \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

# Add user, create home dir
# https://gist.github.com/alkrauss48/2dd9f9d84ed6ebff9240ccfa49a80662
RUN mkdir -p /home/app
RUN groupadd -r app &&\
    useradd -r -g app -d /home/app -s /sbin/nologin -c "docker user" app
ENV HOME=/home/app
WORKDIR ${HOME}

ARG POCKETBOOK_SDK_COMMIT=64e9fa210dd90569f8eabea92da4d40f3d072fc1
RUN git clone https://github.com/viralpoetry/pocketbook-sdk ${HOME}/development/pocketbook-sdk-fw4 \
 && git -C ${HOME}/development/pocketbook-sdk-fw4 checkout ${POCKETBOOK_SDK_COMMIT}
ENV FRSCSDK="${HOME}/development/pocketbook-sdk-fw4/FRSCSDK"

# Build our app
FROM builder AS build_app
COPY app/main.cpp ./main.cpp
COPY tools/sleep-probe/main.cpp ./sleep-probe.cpp
RUN ${FRSCSDK}/bin/arm-none-linux-gnueabi-g++ \
 -Wall -Wextra -Wmissing-field-initializers -Wshadow -Wno-unused-parameter -Wno-unused-function \
 -O2 main.cpp -o pocketframe.app -linkview -s
RUN ${FRSCSDK}/bin/arm-none-linux-gnueabi-g++ \
 -Wall -Wextra -Wmissing-field-initializers -Wshadow -Wno-unused-parameter -Wno-unused-function \
 -O2 sleep-probe.cpp -o sleep-probe.app -linkview -s
