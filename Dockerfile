# Build the single static binary.
FROM golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
COPY rt ./rt
COPY farm ./farm
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /renderfarm .

# Fetch the third-party scene assets and verify them against SHA256SUMS.
FROM alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f AS assets
RUN apk add --no-cache curl
COPY assets/fetch.sh assets/SHA256SUMS /fetch/
RUN sh /fetch/fetch.sh /assets

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /renderfarm /renderfarm
COPY --from=assets /assets /assets
ENV ASSETS=/assets DATA_DIR=/data
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/renderfarm"]
CMD ["coordinator"]
