FROM golang:1.25.12-alpine@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/pocketframe-server .

FROM scratch

COPY --from=build /out/pocketframe-server /pocketframe-server
ENV POCKETFRAME_FRAME_PATH=/data/frame.jpg
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/pocketframe-server"]
