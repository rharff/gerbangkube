FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gerbangkube ./cmd/gerbangkube

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gerbangkube /gerbangkube
USER 65532:65532
ENTRYPOINT ["/gerbangkube"]
